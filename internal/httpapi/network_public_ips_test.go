package httpapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"ocicore/internal/cryptobox"
	"ocicore/internal/ociclient"
	"ocicore/internal/store"
)

func TestListBoundPublicIPsIncludesSecondaryReservedAndSecondaryVnic(t *testing.T) {
	const region = "ap-chuncheon-1"
	routes := map[string]string{
		"/publicIps": `[
			{"id":"pub-sec-1","displayName":"proxy1-res","ipAddress":"152.67.2.2","lifetime":"RESERVED","scope":"REGION","privateIpId":"priv-sec-1","lifecycleState":"ASSIGNED"},
			{"id":"pub-pri-2","displayName":"box2-res","ipAddress":"158.180.1.9","lifetime":"RESERVED","scope":"REGION","assignedEntityId":"ocid1.privateip.oc1..pri2","lifecycleState":"ASSIGNED"},
			{"id":"pub-free","displayName":"free","ipAddress":"152.67.9.9","lifetime":"RESERVED","scope":"REGION","lifecycleState":"AVAILABLE"}
		]`,
		"/privateIps/priv-sec-1":                `{"id":"priv-sec-1","vnicId":"vnic-1","ipAddress":"10.0.0.12","isPrimary":false,"displayName":"sec-1"}`,
		"/privateIps/ocid1.privateip.oc1..pri2": `{"id":"ocid1.privateip.oc1..pri2","vnicId":"vnic-2","ipAddress":"10.0.0.20","isPrimary":true}`,
		"/vnicAttachments": `[
			{"id":"att-1","instanceId":"inst-1","vnicId":"vnic-1","lifecycleState":"ATTACHED"},
			{"id":"att-1b","instanceId":"inst-1","vnicId":"vnic-1b","lifecycleState":"ATTACHED"},
			{"id":"att-2","instanceId":"inst-2","vnicId":"vnic-2","lifecycleState":"ATTACHED"}
		]`,
		"/vnics/vnic-1b": `{"id":"vnic-1b","displayName":"extra-nic","privateIp":"10.0.1.5","publicIp":"168.107.9.8","isPrimary":false}`,
	}

	ociSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:]
		if path == "/privateIps" {
			t.Errorf("unexpected ListPrivateIPs call on %s", r.URL.RawQuery)
		}
		if path == "/privateIps/ocid1.privateip.oc1..pri2" {
			t.Errorf("primary reserved IP already matches cached inst.PublicIP and should skip GetPrivateIP")
		}
		body, ok := routes[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer ociSrv.Close()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(ociSrv.URL)
	client, err := ociclient.New(&ociclient.Credentials{
		TenancyOCID: "ocid1.tenancy.oc1..t",
		UserOCID:    "ocid1.user.oc1..u",
		Fingerprint: ociclient.FingerprintOf(&rsaKey.PublicKey),
		Region:      region,
		PrivateKey:  rsaKey,
	}, ociclient.WithHTTPClient(&http.Client{Transport: rewriteTransport{target}}))
	if err != nil {
		t.Fatal(err)
	}

	boxKey := make([]byte, cryptobox.KeySize)
	if _, err := rand.Read(boxKey); err != nil {
		t.Fatal(err)
	}
	box, err := cryptobox.New(boxKey)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	acc, err := st.CreateAccount(t.Context(), store.NewAccount{
		Alias:           "CHU",
		Code:            "CHU",
		TenancyOCID:     "ocid1.tenancy.oc1..t",
		CompartmentOCID: "ocid1.tenancy.oc1..t",
		UserOCID:        "ocid1.user.oc1..u",
		Fingerprint:     ociclient.FingerprintOf(&rsaKey.PublicKey),
		PrivateKeyPEM:   string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})),
		DefaultRegion:   region,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range []store.Instance{
		{
			ID:             "inst-1",
			AccountID:      acc.ID,
			Region:         region,
			DisplayName:    "proxy1",
			LifecycleState: "RUNNING",
			PublicIP:       "168.107.62.94",
			PrivateIP:      "10.0.0.10",
			SecondaryIPv4:  "168.107.9.8,152.67.2.2",
			VnicID:         "vnic-1",
		},
		{
			ID:             "inst-2",
			AccountID:      acc.ID,
			Region:         region,
			DisplayName:    "proxy2",
			LifecycleState: "STOPPED",
			PublicIP:       "158.180.1.9",
			PrivateIP:      "10.0.0.20",
			VnicID:         "vnic-2",
		},
	} {
		if err := st.UpsertInstance(t.Context(), inst); err != nil {
			t.Fatal(err)
		}
	}

	srv := &Server{st: st}
	got, err := srv.listBoundPublicIPs(t.Context(), client, acc, region)
	if err != nil {
		t.Fatalf("listBoundPublicIPs failed: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 bound public IPs, got %d: %+v", len(got), got)
	}

	byIP := make(map[string]boundPublicIP, len(got))
	for _, row := range got {
		byIP[row.IPAddress] = row
	}

	pri1 := byIP["168.107.62.94"]
	if pri1.InstanceName != "proxy1" || !pri1.IsPrimaryVnic || !pri1.IsPrimaryPrivateIP || pri1.Lifetime != ociclient.PublicIPEphemeral {
		t.Fatalf("unexpected primary ephemeral row: %+v", pri1)
	}

	secRes := byIP["152.67.2.2"]
	if secRes.InstanceName != "proxy1" || !secRes.IsPrimaryVnic || secRes.IsPrimaryPrivateIP || secRes.Lifetime != ociclient.PublicIPReserved || secRes.PrivateIPID != "priv-sec-1" || secRes.PrivateIP != "10.0.0.12" || secRes.VnicID != "vnic-1" {
		t.Fatalf("unexpected secondary private reserved row: %+v", secRes)
	}

	secVnic := byIP["168.107.9.8"]
	if secVnic.InstanceName != "proxy1" || secVnic.IsPrimaryVnic || !secVnic.IsPrimaryPrivateIP || secVnic.Lifetime != ociclient.PublicIPEphemeral || secVnic.VnicID != "vnic-1b" || secVnic.VnicName != "extra-nic" {
		t.Fatalf("unexpected secondary VNIC row: %+v", secVnic)
	}

	priRes := byIP["158.180.1.9"]
	if priRes.InstanceName != "proxy2" || !priRes.IsPrimaryVnic || !priRes.IsPrimaryPrivateIP || priRes.Lifetime != ociclient.PublicIPReserved || priRes.PrivateIPID != "ocid1.privateip.oc1..pri2" || priRes.InstanceState != "STOPPED" {
		t.Fatalf("unexpected primary reserved row: %+v", priRes)
	}

	resList, err := srv.listReservedPublicIPs(t.Context(), client, acc, region)
	if err != nil {
		t.Fatalf("listReservedPublicIPs failed: %v", err)
	}
	if len(resList) != 3 {
		t.Fatalf("expected 3 reserved IPs (2 bound + 1 free), got %d: %+v", len(resList), resList)
	}
	resByIP := make(map[string]boundPublicIP, len(resList))
	for _, row := range resList {
		resByIP[row.IPAddress] = row
	}
	if resByIP["152.67.9.9"].InstanceID != "" || resByIP["152.67.9.9"].PrivateIPID != "" {
		t.Fatalf("expected free reserved IP to be unbound: %+v", resByIP["152.67.9.9"])
	}
	if resByIP["152.67.2.2"].InstanceName != "proxy1" || resByIP["152.67.2.2"].IsPrimaryPrivateIP {
		t.Fatalf("expected bound secondary reserved IP metadata: %+v", resByIP["152.67.2.2"])
	}
}
