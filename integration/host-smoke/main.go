// Load the real native plugin with the public CLIProxyAPI host SDK.
// Only synthetic fixture files may be passed; this never executes upstream requests.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginhost"
)

func run() error {
	if len(os.Args) < 4 {
		return fmt.Errorf("usage: host-smoke PLUGIN_DIR CLAUDE_SYNTHETIC_AUTH KIMI_SYNTHETIC_AUTH")
	}
	authDir, err := os.MkdirTemp("", "web-accounts-host-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(authDir)
	enabled := true
	host := pluginhost.New()
	defer host.ShutdownAll()
	host.ApplyConfig(context.Background(), pluginhost.RuntimeConfig{
		Enabled: true, Dir: os.Args[1], AuthDir: authDir,
		Configs: map[string]pluginhost.PluginInstanceConfig{"web-accounts": {Enabled: &enabled}},
	})
	if len(host.RegisteredPlugins()) != 1 || !host.HasAuthProvider("web-accounts") {
		return fmt.Errorf("native plugin did not register its auth provider")
	}
	for i, path := range os.Args[2:] {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		auth, handled, err := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{
			Provider: "web-accounts", Path: filepath.Join(authDir, filepath.Base(path)),
			FileName: filepath.Base(path), RawJSON: raw,
		})
		if err != nil || !handled || auth == nil {
			return fmt.Errorf("fixture %d: auth parsing failed", i)
		}
		if auth.Provider != "web-accounts" {
			return fmt.Errorf("fixture %d: incorrect provider", i)
		}
		result := host.ModelsForAuth(context.Background(), auth)
		if result.Err != nil || !result.Handled || len(result.Models) == 0 {
			return fmt.Errorf("fixture %d: auth-bound models unavailable", i)
		}
		for _, model := range result.Models {
			lower := strings.ToLower(model.ID)
			if strings.Contains(lower, "qwen") || (i == 0 && strings.Contains(lower, "kimi")) || (i == 1 && strings.Contains(lower, "claude")) {
				return fmt.Errorf("fixture %d: crossed provider model boundary", i)
			}
		}
		fmt.Printf("PASS: fixture %d parsed by native host; %d account-scoped models\n", i, len(result.Models))
	}
	fmt.Println("PASS: actual dylib load, plugin registration, auth parsing and model isolation; no upstream calls")
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
