package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ======================================================================================
// R4 EMPIRICAL TESTS: Dockerfile CA-Certificates Invariants
// (Rävn task 01M3MMZYHN503HZY11V1V1G2RF)
// ======================================================================================
//
// Go's crypto/x509 has no built-in root-certificate bundle, so an alpine
// runtime image that lacks the ca-certificates package cannot validate any
// outbound TLS handshake: ravn-server (OPENCODE_SERVER_URL=https://...) and
// ravn-mcp (RAVN_API_URL=https://...) would fail with "x509:
// certificate signed by unknown authority". Every alpine runtime stage must
// therefore install ca-certificates via apk WHILE STILL ROOT (before any USER
// directive drops privileges).
//
// TestChallenger_R4_DockerfilesCACertificates statically audits Dockerfile,
// Dockerfile.server, and Dockerfile.mcp. It joins shell line-continuations
// into logical statements first, so the audit is robust to formatting: the
// apk add may sit in its own RUN or be merged into an existing multi-command
// RUN via &&, and package names may wrap onto continuation lines.

type dockerStage struct {
	file        string
	stageIndex  int
	baseImage   string
	stageName   string
	isRuntime   bool
	hasCACerts  bool
	caCertLine  int // line index where the apk-add-ca-certificates statement starts (-1 if none)
	userAppLine int // line index of the last USER statement (-1 if none)
}

// apkAddCACerts matches an apk add that installs ca-certificates, tolerating
// apk flags before and/or after "add" (e.g. `apk add --no-cache ...` and
// `apk --no-cache add ...`). It is applied to continuation-joined statements,
// so multi-command RUNs (`apk update && apk add --no-cache ca-certificates`)
// match as well.
var apkAddCACerts = regexp.MustCompile(`apk\s+(?:-\S+\s+)*add\s+(?:-\S+\s+)*ca-certificates`)

// TestChallenger_R4_DockerfilesCACertificates programmatically audits Dockerfile,
// Dockerfile.server, and Dockerfile.mcp to verify that all alpine runtime stages
// across the multi-stage and standalone Dockerfiles install ca-certificates with
// root privileges before switching to unprivileged USER app. Any alpine runtime
// stage missing the package (including one added after this test was written)
// fails the test and is reported by file/stage.
func TestChallenger_R4_DockerfilesCACertificates(t *testing.T) {
	rootPath := filepath.Join("..", "..")
	dockerfiles := []string{
		filepath.Join(rootPath, "Dockerfile"),
		filepath.Join(rootPath, "Dockerfile.server"),
		filepath.Join(rootPath, "Dockerfile.mcp"),
	}

	totalRuntimeStages := 0

	for _, dfPath := range dockerfiles {
		content, err := os.ReadFile(dfPath)
		if err != nil {
			t.Fatalf("failed to read %s: %v", dfPath, err)
		}

		lines := strings.Split(string(content), "\n")
		var stages []dockerStage
		var cur *dockerStage // stage the current line belongs to (nil before first FROM)

		var buf string // in-progress logical statement (continuations joined)
		startIdx := -1 // line index where `buf` began
		inCont := false

		fromRe := regexp.MustCompile(`(?i)^\s*FROM\s+(\S+)(?:\s+AS\s+(\S+))?`)

		evalStmt := func(start int, stmt string) {
			if cur == nil || strings.TrimSpace(stmt) == "" {
				return
			}
			fields := strings.Fields(stmt)
			switch strings.ToUpper(fields[0]) {
			case "USER":
				cur.userAppLine = start // last USER wins (must precede any privilege drop)
			case "RUN":
				if !cur.hasCACerts && apkAddCACerts.MatchString(stmt) {
					cur.hasCACerts = true
					cur.caCertLine = start
				}
			}
		}

		for lineIdx, raw := range lines {
			trimmed := strings.TrimSpace(strings.TrimRight(raw, "\r"))

			if !inCont {
				if m := fromRe.FindStringSubmatch(trimmed); m != nil {
					base := m[1]
					name := ""
					if len(m) > 2 {
						name = m[2]
					}
					stage := dockerStage{
						file:        filepath.Base(dfPath),
						stageIndex:  len(stages),
						baseImage:   base,
						stageName:   name,
						caCertLine:  -1,
						userAppLine: -1,
					}
					// Stage classification:
					// Dockerfile: web-build (node:*-alpine) and go-build (golang:*-alpine)
					// are builders; server-runtime and mcp-runtime (plain alpine) are runtime.
					// Dockerfile.server: same pattern, unnamed final alpine stage is runtime.
					// Dockerfile.mcp: go-build is builder; unnamed final alpine stage is runtime.
					if strings.Contains(strings.ToLower(base), "alpine") &&
						!strings.Contains(strings.ToLower(name), "build") &&
						!strings.Contains(strings.ToLower(base), "node") &&
						!strings.Contains(strings.ToLower(base), "golang") {
						stage.isRuntime = true
					}
					stages = append(stages, stage)
					cur = &stages[len(stages)-1]
					continue
				}
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
			}

			isCont := strings.HasSuffix(trimmed, "\\")
			stmt := strings.TrimSpace(strings.TrimSuffix(trimmed, "\\"))
			if !inCont {
				buf, startIdx = stmt, lineIdx
			} else if stmt != "" {
				buf += " " + stmt
			}
			inCont = isCont
			if !isCont {
				evalStmt(startIdx, buf)
			}
		}
		if inCont && cur != nil { // defensive: dangling continuation at EOF
			evalStmt(startIdx, buf)
		}

		for _, st := range stages {
			if st.isRuntime {
				totalRuntimeStages++
				if !st.hasCACerts {
					t.Errorf("[%s stage %d (%s)] MISSING ca-certificates installation in runtime stage",
						st.file, st.stageIndex, st.stageName)
				}
				if st.userAppLine != -1 && st.caCertLine > st.userAppLine {
					t.Errorf("[%s stage %d (%s)] ca-certificates installed after USER command (requires root)",
						st.file, st.stageIndex, st.stageName)
				}
			}
		}
	}

	if totalRuntimeStages != 4 {
		t.Fatalf("expected exactly 4 Alpine runtime stages across the three Dockerfiles "+
			"(server-runtime, mcp-runtime, and the final stages of Dockerfile.server and "+
			"Dockerfile.mcp), found %d — if a new runtime stage was added deliberately, "+
			"install ca-certificates in it and update this count", totalRuntimeStages)
	}
}
