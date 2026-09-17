package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestRegistrationEnvelopeCarriesConfigFields(t *testing.T) {
	raw, err := handleRegister([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("registration failed: %#v", envelope.Error)
	}
	var registration abiRegistration
	if err := json.Unmarshal(envelope.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if len(registration.Metadata.ConfigFields) == 0 {
		t.Fatal("registration metadata has zero config fields")
	}
	metaRaw, _ := json.Marshal(registration.Metadata)
	t.Logf("registration metadata JSON: %s", metaRaw)
}
