package projector

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ingest"
)

// goldenBatch is the committed §4.10 conformance batch, relative to this package.
const goldenBatch = "../../../contracts/testdata/batches/batch-001.ndjson"

// TestGoldenBatchIsAtTheCurrentVersions is the Go half of the drift check the
// conformance layer exists to run.
//
// `contracts/testdata` is generated with a hard-coded `ver` per line, so nothing
// else notices when a type bumps here and the fixture does not — and a fixture
// left behind pins a payload shape the mod stopped emitting, which is the exact
// failure mode a cross-language vector set is supposed to make impossible. The
// C# suite asserts the mirror of this against `EventTypes.VersionOf`, so the two
// registries and the vectors are pinned to each other from both sides.
func TestGoldenBatchIsAtTheCurrentVersions(t *testing.T) {
	f, err := os.Open(filepath.FromSlash(goldenBatch))
	if os.IsNotExist(err) {
		t.Skip("contracts/testdata is not present in this checkout")
	}
	if err != nil {
		t.Fatalf("open the golden batch: %v", err)
	}
	defer f.Close()

	seen := map[string]struct{}{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var env struct {
			Type string `json:"type"`
			Ver  int    `json:"ver"`
		}
		if err := json.Unmarshal(sc.Bytes(), &env); err != nil {
			t.Fatalf("line %d is not JSON: %v", line, err)
		}
		if !ingest.KnownType(env.Type) {
			t.Errorf("line %d: %q is not in the §4.2 registry", line, env.Type)
			continue
		}
		want := CurrentVer
		if v, ok := currentVer[env.Type]; ok {
			want = v
		}
		if env.Ver != want {
			t.Errorf("line %d: %s is ver %d in the vector, this build folds ver %d\n"+
				"regenerate with `make testvectors` after bumping a version",
				line, env.Type, env.Ver, want)
		}
		seen[env.Type] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	// Every type the version map overrides is a type whose shape moved, so it is
	// exactly the set the vectors must not be allowed to stop covering.
	for typ := range currentVer {
		if _, ok := seen[typ]; !ok {
			t.Errorf("%s is at ver %d but appears in no vector line", typ, currentVer[typ])
		}
	}
}
