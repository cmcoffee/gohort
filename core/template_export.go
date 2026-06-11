package core

import (
	"fmt"
	"regexp"
	"time"
)

// BuildTemplate assembles a TemplateBundle from live artifacts and makes it
// safe and portable to check into the repo. It is the export counterpart to
// ParseTemplate and enforces the two invariants the on-disk format relies on:
//
//  1. No secret travels. Credentials are secret-free by struct shape, but a
//     sloppily-authored tool can hardcode a key into its command/body/pipe/
//     script template instead of dispatching through its credential.
//     BuildTemplate scans those fields and REFUSES rather than ship one.
//  2. No instance state travels. Runtime timestamps and computed flags are
//     cleared so the template is a clean recipe, not a snapshot of one
//     deployment.
//
// requires_secrets is auto-derived from the credentials (each needs its
// secret filled at import); the caller can refine labels/help afterward.
func BuildTemplate(manifest TemplateManifest, creds []SecureCredential, tools []TempTool, skills []SkillRecord) (TemplateBundle, error) {
	// Refuse any tool that hardcodes a secret instead of using a credential.
	for _, t := range tools {
		fields := map[string]string{
			"command_template": t.CommandTemplate,
			"body_template":    t.BodyTemplate,
			"response_pipe":    t.ResponsePipe,
			"script_body":      t.ScriptBody,
		}
		for field, val := range fields {
			if scanForEmbeddedSecret(val) {
				return TemplateBundle{}, fmt.Errorf("tool %q looks like it hardcodes a secret in %s; route it through a credential before exporting", t.Name, field)
			}
		}
	}

	// Sanitize credentials: keep config + policy, drop instance state.
	cleanCreds := make([]SecureCredential, 0, len(creds))
	for _, c := range creds {
		c.CreatedAt = time.Time{}
		c.LastUsedAt = time.Time{}
		c.Pending = false
		c.Disabled = false
		cleanCreds = append(cleanCreds, c)
	}

	// Auto-derive the required-secrets list, skipping any the caller named.
	declared := make(map[string]bool, len(manifest.RequiresSecrets))
	for _, s := range manifest.RequiresSecrets {
		declared[s.Credential] = true
	}
	for _, c := range cleanCreds {
		if declared[c.Name] {
			continue
		}
		manifest.RequiresSecrets = append(manifest.RequiresSecrets, TemplateSecret{
			Credential: c.Name,
			Label:      secretLabelFor(c),
		})
	}

	b := TemplateBundle{
		Manifest:    manifest,
		Credentials: cleanCreds,
		Tools:       tools,
		Skills:      skills,
	}
	// Round-trip through the validator so export can never emit something
	// import would reject.
	data, err := MarshalTemplate(b)
	if err != nil {
		return TemplateBundle{}, err
	}
	return ParseTemplate(data)
}

// secretLabelFor produces a sensible default prompt label for a credential's
// missing secret, based on its auth type.
func secretLabelFor(c SecureCredential) string {
	if c.Type == "oauth2" {
		return c.Name + " OAuth client secret"
	}
	return c.Name + " API key / token"
}

// secretLikeRe matches literal secret-shaped values while leaving
// {placeholder} substitution slots alone: a value that starts with "{" (the
// templating convention) is never flagged. Catches "Bearer <literal>" and
// "<key>=<literal>" forms, which are the common ways a hand-authored tool
// smuggles a credential it should have routed through SecureAPI.
var secretLikeRe = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._\-]{12,}|(api[_-]?key|token|secret|password|access[_-]?token)\s*[=:]\s*[^{\s][a-z0-9._/+\-]{7,})`)

// scanForEmbeddedSecret reports whether s contains a literal secret-shaped
// value. Heuristic by design: it errs toward catching obvious baked-in
// tokens while ignoring the {placeholder} slots templates legitimately use.
func scanForEmbeddedSecret(s string) bool {
	if s == "" {
		return false
	}
	return secretLikeRe.MatchString(s)
}

// ContainsLikelySecret reports whether s holds a literal secret-shaped value
// (Bearer tokens, key=… / token: … forms). Exported wrapper over the template
// scanner so other packages — e.g. history archiving — can refuse to index
// credential-bearing text.
func ContainsLikelySecret(s string) bool { return scanForEmbeddedSecret(s) }
