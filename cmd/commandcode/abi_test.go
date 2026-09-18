package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestDecodeABIAuthModelRequestInjectsHostHTTPClient(t *testing.T) {
	raw, err := json.Marshal(abiAuthModelRequest{
		AuthModelRequest: pluginapi.AuthModelRequest{
			AuthID:     "auth-1",
			Attributes: map[string]string{"api_key": "key"},
		},
		HostCallbackID: "callback-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := decodeABIAuthModelRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := req.HTTPClient.(abiHostHTTPClient)
	if !ok {
		t.Fatalf("HTTPClient type = %T, want abiHostHTTPClient", req.HTTPClient)
	}
	if client.callbackID != "callback-42" {
		t.Fatalf("callback ID = %q, want callback-42", client.callbackID)
	}
	if req.AuthID != "auth-1" || req.Attributes["api_key"] != "key" {
		t.Fatalf("decoded auth request = %#v", req)
	}
}

func TestAuthMethodIsReachable(t *testing.T) {
	registerRequest, errMarshalRegister := json.Marshal(abiLifecycleRequest{ConfigYAML: []byte("shared_scheduling: true\n")})
	if errMarshalRegister != nil {
		t.Fatal(errMarshalRegister)
	}
	if _, err := handleRegister(registerRequest); err != nil {
		t.Fatal(err)
	}
	authRequest, errMarshal := json.Marshal(pluginapi.AuthParseRequest{
		Provider: "commandcode",
		FileName: "commandcode.json",
		RawJSON:  json.RawMessage(`{"type":"commandcode","api_key":"user_test"}`),
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	rawAuth, errAuth := handleABIMethod(nil, pluginabi.MethodAuthParse, authRequest)
	if errAuth != nil {
		t.Fatal(errAuth)
	}
	var authEnvelope pluginabi.Envelope
	if errDecode := json.Unmarshal(rawAuth, &authEnvelope); errDecode != nil || !authEnvelope.OK {
		t.Fatalf("auth response = %s (%v)", rawAuth, errDecode)
	}

}

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
	if !registration.Capabilities.AuthProvider {
		t.Fatal("registration did not advertise AuthProvider")
	}
	metaRaw, _ := json.Marshal(registration.Metadata)
	t.Logf("registration metadata JSON: %s", metaRaw)
}
