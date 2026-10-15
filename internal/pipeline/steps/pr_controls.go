package steps

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// Published PR text must be a fixed point of the read path. `gh pr view --json`
// (go-gh's asciisanitizer) rewrites C0 and C1 control characters, and the
// six-character JSON escapes that decode to them, into caret notation. The
// pr.template integrity checks compare the body gh reads back with the body that
// was sent, and the appendix digest is stamped over the sent bytes, so any such
// byte makes both fail with a false "edited" refusal. neutralizePRControls runs
// at the publication boundary, before the digest is stamped.

var (
	// ansiSequencePattern matches whole terminal escape sequences: CSI, OSC
	// (terminated by BEL or ST) and two-byte escapes. They are dropped rather
	// than rewritten so captured output stays readable.
	ansiSequencePattern = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)
	// jsonControlEscapePattern matches the literal text `\u001b` (any case) that
	// gh also rewrites, because it cannot tell it from an encoded control byte.
	// Captured JSON output carries it routinely.
	jsonControlEscapePattern = regexp.MustCompile(`(?i)\\u00(0[0-8cef]|1[0-9a-f]|[89][0-9a-f])`)
)

// neutralizePRControls removes terminal escape sequences and replaces every
// other control character gh would rewrite with U+FFFD. Tab, newline, vertical
// tab and carriage return are untouched. Literal `\u00XX` escapes of the
// rewritten characters become `\u{00XX}`. It is idempotent.
func neutralizePRControls(s string) string {
	s = ansiSequencePattern.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if rewrittenByGH(r) {
			return '�'
		}
		return r
	}, s)
	return jsonControlEscapePattern.ReplaceAllStringFunc(s, func(m string) string {
		return `\u{` + m[2:] + `}`
	})
}

func rewrittenByGH(r rune) bool {
	switch {
	case r == '\t', r == '\n', r == '\v', r == '\r':
		return false
	case r < 0x20:
		return true
	}
	return r >= 0x80 && r <= 0x9f
}

// restoreCaretControls maps caret notation back to the C0 control characters gh
// produces it from, so an appendix that an earlier version stamped over raw
// control bytes can still be recognised as ours. C1 characters share their
// caret form with C0 and are restored as C0.
func restoreCaretControls(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '^' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		c := s[i+1]
		switch {
		case c >= '@' && c <= '_' && c != 'I' && c != 'J' && c != 'K' && c != 'M':
			b.WriteByte(c - '@')
			i++
		default:
			b.WriteByte('^')
		}
	}
	return b.String()
}

// appendixDigestMatches reports whether appendix carries the stamped digest,
// either byte for byte or after undoing gh's caret rewriting of control bytes
// that an earlier version embedded.
func appendixDigestMatches(appendix, digest string) bool {
	if fmt.Sprintf("%x", sha256.Sum256([]byte(appendix))) == digest {
		return true
	}
	restored := restoreCaretControls(appendix)
	return restored != appendix && fmt.Sprintf("%x", sha256.Sum256([]byte(restored))) == digest
}
