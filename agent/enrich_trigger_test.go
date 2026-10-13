package agent

import (
	"testing"
)

type enrichmentRecorder struct{ dns, http int }

func (r *enrichmentRecorder) ResolveDomain(int64, string) { r.dns++ }
func (r *enrichmentRecorder) ProbeSite(int64, string)     { r.http++ }

func TestInsertAssetsTriggersEnrichment(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	defer d.Exec(`DELETE FROM assets WHERE domain='probe.enrich-test.invalid' OR root_domain='enrich-test.invalid'`)
	ts := NewToolSet(nil, "")
	ts.SetAssetStore(d.Assets(), d.Companies())
	recorder := &enrichmentRecorder{}
	ts.SetEnrich(recorder)
	out := callInsertAssets(t, ts, map[string]any{"assets": []any{
		map[string]any{"type": "subdomain", "domain": "probe.enrich-test.invalid"},
		map[string]any{"type": "service", "url": "http://probe.enrich-test.invalid"},
	}})
	if rows, _ := out["results"].([]any); len(rows) != 2 {
		t.Fatalf("登记失败: %v", out)
	}
	if recorder.dns != 1 || recorder.http != 1 {
		t.Fatalf("资产补全未触发: DNS=%d HTTP=%d", recorder.dns, recorder.http)
	}
}
