package main

import (
	"bidmon/internal/evidence"
	"context"
	"encoding/json"
	"io"
	"os"
	"time"
)

func main() {
	go func() { time.Sleep(10 * time.Second); os.Exit(2) }()
	deny := func() { _, _ = os.Stdout.WriteString(`{"error":"request_denied"}`); os.Exit(2) }
	command := os.Getenv("SSH_ORIGINAL_COMMAND")
	if command != "presales-diag" && command != "bid-mon-evidence" {
		deny()
	}
	raw, e := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if e != nil {
		deny()
	}
	keyPath := os.Getenv("BID_MON_HELPER_BINDING_KEY_FILE")
	if keyPath == "" {
		keyPath = "/ssh-target/binding.key"
	}
	key, e := os.ReadFile(keyPath)
	if e != nil {
		deny()
	}
	if r, err := evidence.ValidateProfile(raw, key, time.Now()); err == nil {
		profilePath := os.Getenv("BID_MON_HELPER_PROFILES_FILE")
		if profilePath == "" {
			// sshd reconstructs the forced-command session environment, so the
			// normal container environment cannot be relied on here.
			profilePath = "/etc/bid-mon-helper/profiles.json"
		}
		profiles, err := evidence.ReadProfiles(profilePath)
		if err != nil {
			deny()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
		defer cancel()
		_ = json.NewEncoder(os.Stdout).Encode(evidence.CollectProfile(ctx, r, profiles, time.Now()))
		return
	}
	r, e := evidence.Validate(raw, key, time.Now())
	if e != nil {
		deny()
	}
	_ = json.NewEncoder(os.Stdout).Encode(evidence.Collect(r, "/evidence", time.Now()))
}
