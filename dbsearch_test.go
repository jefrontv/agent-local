package main

import "testing"

func TestParseSearchReplaceTableDryRunSummary(t *testing.T) {
	out := `+------------------+--------------+--------------+------+
| Table            | Column       | Replacements | Type |
+------------------+--------------+--------------+------+
| wp_options       | option_value | 3            | PHP  |
| wp_posts         | post_content | 12           | SQL  |
| wp_postmeta      | meta_value   | 0            | PHP  |
+------------------+--------------+--------------+------+
Success: 15 replacements to be made.
`
	hits, total := parseSearchReplaceTable(out)
	if len(hits) != 2 {
		t.Fatalf("expected 2 non-zero hits, got %d: %+v", len(hits), hits)
	}
	if hits[0].Table != "wp_options" || hits[0].Column != "option_value" || hits[0].Count != 3 {
		t.Fatalf("bad first hit: %+v", hits[0])
	}
	if hits[1].Table != "wp_posts" || hits[1].Column != "post_content" || hits[1].Count != 12 {
		t.Fatalf("bad second hit: %+v", hits[1])
	}
	if total != 15 {
		t.Fatalf("expected total 15 from Success line, got %d", total)
	}
}

func TestParseSearchReplaceTableMadeSummary(t *testing.T) {
	out := `+------------------+--------------+--------------+------+
| Table            | Column       | Replacements | Type |
+------------------+--------------+--------------+------+
| wp_options       | option_value | 743          | PHP  |
| wp_posts         | post_content | 12           | SQL  |
+------------------+--------------+--------------+------+
Success: Made 755 replacements.
`
	hits, total := parseSearchReplaceTable(out)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if total != 755 {
		t.Fatalf("expected total 755, got %d", total)
	}
}

func TestParseSearchReplaceTableSumFallback(t *testing.T) {
	// No "Success:" line at all - total must fall back to summing the rows.
	out := `+------------------+--------------+--------------+------+
| Table            | Column       | Replacements | Type |
+------------------+--------------+--------------+------+
| wp_options       | option_value | 3            | PHP  |
| wp_posts         | post_content | 12           | SQL  |
+------------------+--------------+--------------+------+
`
	hits, total := parseSearchReplaceTable(out)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if total != 15 {
		t.Fatalf("expected summed total 15, got %d", total)
	}
}

func TestParseSearchReplaceTableEmpty(t *testing.T) {
	hits, total := parseSearchReplaceTable("Success: 0 replacements to be made.\n")
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %+v", hits)
	}
	if total != 0 {
		t.Fatalf("expected total 0, got %d", total)
	}
}

// On a pipe - which is what wp-cli always sees from the daemon - --format=table
// is tab-separated with no box drawing. Captured from a live run.
func TestParseSearchReplaceTablePipedTSV(t *testing.T) {
	out := "Table\tColumn\tReplacements\tType\n" +
		"wp_gf_entry\tsource_url\t2\tSQL\n" +
		"wp_itsec_logs\turl\t36\tSQL\n" +
		"wp_options\toption_value\t6\tPHP\n" +
		"Success: 44 replacements to be made.\n"
	hits, total := parseSearchReplaceTable(out)
	if len(hits) != 3 || hits[0].Table != "wp_gf_entry" || hits[0].Column != "source_url" || hits[0].Count != 2 {
		t.Fatalf("hits = %+v", hits)
	}
	if total != 44 {
		t.Fatalf("total = %d, want 44", total)
	}
}

func TestIsHostLike(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"ssp.c1.efront.dev", true},
		{"https://ssp.c1.efront.dev", true},
		{"https://ssp.c1.efront.dev:10443", true},
		{"hello world", false},
		{"localhost", true},
		{"localhost:1080", true},
		{"staging", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isHostLike(c.in); got != c.want {
			t.Errorf("isHostLike(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
