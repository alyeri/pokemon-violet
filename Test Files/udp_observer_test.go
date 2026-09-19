package main

import (
	"reflect"
	"testing"
)

func TestUDPObserverDefaultIncludesObservedPiaStagePort(t *testing.T) {
	t.Setenv("NPLN_UDP_OBSERVE_ADDRS", "")

	want := []string{"0.0.0.0:443", "127.0.0.1:34343"}
	if got := configuredUDPObserverAddresses(); !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredUDPObserverAddresses() = %v, want %v", got, want)
	}
}

func TestUDPObserverOverrideDeduplicatesAddresses(t *testing.T) {
	t.Setenv("NPLN_UDP_OBSERVE_ADDRS", "127.0.0.1:34343, 127.0.0.1:40000,127.0.0.1:34343")

	want := []string{"127.0.0.1:34343", "127.0.0.1:40000"}
	if got := configuredUDPObserverAddresses(); !reflect.DeepEqual(got, want) {
		t.Fatalf("configuredUDPObserverAddresses() = %v, want %v", got, want)
	}
}

func TestUDPObserverCanBeDisabled(t *testing.T) {
	t.Setenv("NPLN_UDP_OBSERVE_ADDRS", "disabled")

	if got := configuredUDPObserverAddresses(); got != nil {
		t.Fatalf("configuredUDPObserverAddresses() = %v, want nil", got)
	}
}

func TestDescribeUDPDatagramRecognizesVioletMonitoringPacket(t *testing.T) {
	packet := make([]byte, 252)
	copy(packet, piaPacketMagic[:])
	packet[4] = 11
	packet[28] = 0x0f // message flags, size, protocol/port and destination
	packet[29] = 0
	packet[30] = 0
	packet[31] = 208
	packet[32] = 0xa4
	packet[44] = 32
	packet[45] = 0 // session-begin monitoring data
	packet[46] = 0xfc
	packet[48] = 0
	packet[49] = 168
	packet[58] = 0x94

	fields := describeUDPDatagram("pia", nil, nil, packet)
	checks := map[string]interface{}{
		"pia_protocol":             "monitoring_data",
		"pia_protocol_id":          0xa4,
		"pia_message_payload_size": 208,
		"monitoring_version":       32,
		"monitoring_data_type":     0,
		"monitoring_declared_size": 168,
		"monitoring_key_id":        0x94,
	}
	for name, want := range checks {
		if got := fields[name]; got != want {
			t.Fatalf("%s = %#v, want %#v", name, got, want)
		}
	}
}
