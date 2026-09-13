package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestAccountsResourceWireContract(t *testing.T) {
	raw, err := handleMethod(methodRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var reg registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatal(err)
	}
	if !reg.Capabilities.ManagementAPI {
		t.Fatal("resource registrar not advertised")
	}
	raw, err = handleMethod(methodManagementRegister, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var routes resourceRegistration
	if err := json.Unmarshal(env.Result, &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes.Resources) != 1 || routes.Resources[0].Menu != "Cuentas web" || routes.Resources[0].Path != accountsResourcePath {
		t.Fatalf("invalid menu: %+v", routes)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", accountsResourcePath, 200},
		{"POST", accountsResourcePath, 405},
		{"GET", accountsResourcePath + "/unknown", 404},
	} {
		request, _ := json.Marshal(resourceRequest{Method: tc.method, Path: tc.path})
		raw, err := handleMethod(methodManagementHandle, request)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
			t.Fatalf("envelope %s: %v", raw, err)
		}
		var response resourceResponse
		if err := json.Unmarshal(env.Result, &response); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.path, response.StatusCode)
		}
		if tc.status == 200 && (!bytes.Equal(response.Body, accountsHTML) || response.Headers.Get("Content-Type") != "text/html; charset=utf-8") {
			t.Fatal("resource content not preserved over wire")
		}
		if response.Headers.Get("Content-Security-Policy") == "" {
			t.Fatal("missing CSP")
		}
	}
	if _, err := handleMethod(methodManagementHandle, []byte(`{`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
