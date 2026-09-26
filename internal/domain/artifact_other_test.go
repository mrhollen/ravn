package domain

import "testing"

// TestArtifactKinds_AllCanonicalValid pins the Slice 2A decision: the kind set
// is log/screenshot/command-output/trace/url plus the 'other' escape hatch
// (migration 0002_artifact_other.sql), and only those.
func TestArtifactKinds_AllCanonicalValid(t *testing.T) {
	canonical := []ArtifactKind{
		ArtifactLog, ArtifactScreenshot, ArtifactCommandOutput,
		ArtifactTrace, ArtifactURL, ArtifactOther,
	}
	for _, k := range canonical {
		if !k.Valid() {
			t.Errorf("kind %q must be Valid", k)
		}
	}
	for _, bad := range []ArtifactKind{"", "bogus", "log ", "Log"} {
		if bad.Valid() {
			t.Errorf("kind %q must not be Valid", bad)
		}
	}
}
