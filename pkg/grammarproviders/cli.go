package grammarproviders

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"chronicle-server/pkg/config"
	"chronicle-server/pkg/grammar"
)

// RunCLI handles `chronicler providers validate|probe` before database startup,
// allowing Compose deployments to use the application image as a preflight.
func RunCLI(cfg *config.Config, args []string) (handled bool, exitCode int) {
	if len(args) == 0 {
		fmt.Println("Usage: chronicler providers validate|probe")
		return true, 2
	}
	switch args[0] {
	case "validate":
		providers, errs := LoadFile(cfg.Grammar.ProvidersFile)
		for _, err := range errs {
			fmt.Printf("invalid: %v\n", err)
		}
		if len(errs) > 0 {
			return true, 1
		}
		fmt.Printf("Valid grammar provider configuration (%d provider(s)).\n", len(providers))
		return true, 0
	case "probe":
		dict, err := grammar.Load()
		if err != nil {
			fmt.Printf("native provider unavailable: %v\n", err)
			return true, 1
		}
		registry := New(cfg, dict)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		items := registry.List(ctx)
		encoded, _ := json.MarshalIndent(map[string]interface{}{"providers": items}, "", "  ")
		fmt.Println(string(encoded))
		for _, item := range items {
			if !item.Available {
				return true, 1
			}
		}
		return true, 0
	default:
		return false, 0
	}
}
