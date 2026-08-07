package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/meow-sci/catlog/server/internal/adminapi"
	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/cjws"
	"github.com/meow-sci/catlog/server/internal/config"
)

// CredentialFileName is what §4.6 calls the file a player downloads. The mod
// looks for `credential.json` in its own directory; this is the download name.
const CredentialFileName = "catlog-credential.json"

// CredentialFormat is the §4.6 `format` version.
const CredentialFormat = 1

// Credential is the §4.6 credential file.
//
// Field order matters only to a human reading it; what matters to the loader is
// that `private_key_pem` never leaves the machine that generated it.
type Credential struct {
	Format        int    `json:"format"`
	Handle        string `json:"handle"`
	License       string `json:"license"`
	PrivateKeyPEM string `json:"private_key_pem"`
}

// runIssue implements `catlogctl issue` (§5.9).
//
// The private key is generated **here**, and only its public JWK is sent, so a
// credential file assembled by this command has the same property as one
// assembled by the browser wizard (§5.7): the server never sees the private
// half. `--jwk` covers the case where the key already exists elsewhere; the
// server then has nothing to return but the license.
//
// Dev and test only: it talks to the unauthenticated loopback admin API.
func runIssue(args []string) error {
	fs := flag.NewFlagSet("issue", flag.ContinueOnError)
	handle := fs.String("handle", "", "handle to issue a credential for (required)")
	out := fs.String("out", ".", "directory to write "+CredentialFileName+" into (- writes JSON to stdout)")
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	jwkPath := fs.String("jwk", "", "path to an existing public JWK; the server then returns only the license")
	timeout := fs.Duration("timeout", 10*time.Second, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl issue -handle NAME [-out DIR] [-admin URL]\n\n"+
			"Generates a P-256 key pair locally, asks the admin API for a license bound to it\n"+
			"(POST /admin/issue, §5.9) and writes a complete "+CredentialFileName+" (§4.6).\n"+
			"The private key never leaves this machine.\n\nDev and test only.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *handle == "" {
		fs.Usage()
		return fmt.Errorf("-handle is required")
	}

	base, err := adminBaseURL(*admin, *cfgPath)
	if err != nil {
		return err
	}

	// Either use the caller's public JWK, or mint a key pair here.
	var (
		req     adminapi.IssueRequest
		privPEM string
	)
	req.Handle = *handle
	if *jwkPath != "" {
		raw, err := os.ReadFile(*jwkPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", *jwkPath, err)
		}
		if _, err := cjws.ParsePublicJWK(raw); err != nil {
			return fmt.Errorf("%s: %w", *jwkPath, err)
		}
		req.JWK = json.RawMessage(bytes.TrimSpace(raw))
	} else {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return fmt.Errorf("generate client key: %w", err)
		}
		if privPEM, err = cjws.MarshalPrivateKeyPEM(key); err != nil {
			return err
		}
		if req.JWK, err = cjws.PublicJWK(&key.PublicKey); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	res, err := postAdmin(ctx, base+"/admin/issue", req)
	if err != nil {
		return err
	}

	// §4.6: the loader refuses to ship if the key's thumbprint is not the
	// license `cnf.jkt`. Check it here too, so a mismatch is caught at issue
	// time rather than on the player's machine.
	if privPEM != "" {
		key, err := cjws.ParsePrivateKeyPEM([]byte(privPEM))
		if err != nil {
			return err
		}
		jkt, err := cjws.ThumbprintPublicKey(&key.PublicKey)
		if err != nil {
			return err
		}
		if jkt != res.JKT {
			return fmt.Errorf("the server bound the license to %s, but the local key thumbprints to %s", res.JKT, jkt)
		}
	}
	if err := checkLicenseBinding(res.License, res.JKT, *handle); err != nil {
		return err
	}

	cred := Credential{
		Format:        CredentialFormat,
		Handle:        res.Handle,
		License:       res.License,
		PrivateKeyPEM: privPEM,
	}
	body, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')

	if *out == "-" {
		_, err := os.Stdout.Write(body)
		return err
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", *out, err)
	}
	path := filepath.Join(*out, CredentialFileName)
	// 0600: the file carries a private key (§4.6).
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	fmt.Printf("credential written: %s\n", abs)
	fmt.Printf("  handle:  %s\n", res.Handle)
	fmt.Printf("  jkt:     %s\n", res.JKT)
	fmt.Printf("  expires: %s\n", time.UnixMilli(res.ExpiresAt).UTC().Format(time.RFC3339))
	if privPEM == "" {
		fmt.Println("  note:    no private key in the file — you supplied the JWK, so you already hold it")
	}
	return nil
}

// checkLicenseBinding decodes the license without verifying it (we have no
// public key here) and checks the claims agree with what we asked for — the
// same "parse to display handle and expiry" step §4.6 requires of the loader.
func checkLicenseBinding(license, jkt, handle string) error {
	_, payload, err := cjws.ParseCompactUnverified(license)
	if err != nil {
		return fmt.Errorf("the server returned an unreadable license: %w", err)
	}
	var claims authz.LicenseClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return fmt.Errorf("the server returned unreadable license claims: %w", err)
	}
	if claims.Cnf.JKT != jkt {
		return fmt.Errorf("license cnf.jkt is %s, want %s", claims.Cnf.JKT, jkt)
	}
	if claims.Handle != handle {
		return fmt.Errorf("license handle is %q, want %q", claims.Handle, handle)
	}
	return nil
}

// adminBaseURL resolves the admin API base: the flag, else the configured
// admin_listen (§3: 127.0.0.1:6060).
func adminBaseURL(flagValue, cfgPath string) (string, error) {
	if flagValue != "" {
		return trimSlash(flagValue), nil
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return "", err
	}
	return "http://" + cfg.Server.AdminListen, nil
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// postAdmin posts a JSON body to the admin API and decodes the §5.9 response,
// turning a §4.9 error body into a Go error.
func postAdmin(ctx context.Context, url string, body any) (adminapi.IssueResponse, error) {
	var out adminapi.IssueResponse

	payload, err := json.Marshal(body)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("POST %s: %w (is catlogd running?)", url, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return out, fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			if e.Detail != "" {
				return out, fmt.Errorf("%s: %s (%s)", url, e.Error, e.Detail)
			}
			return out, fmt.Errorf("%s: %s", url, e.Error)
		}
		return out, fmt.Errorf("%s: HTTP %d: %s", url, res.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("response is not JSON: %w", err)
	}
	return out, nil
}
