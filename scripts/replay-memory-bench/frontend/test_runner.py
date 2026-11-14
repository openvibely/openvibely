"""Small checks for report aggregation; no browser or output files required."""
import unittest

from run import summarize
from mixed_loading import summarize_rows


class SummaryTests(unittest.TestCase):
    def test_http_render_outcomes_are_not_numeric_medians(self):
        summary = summarize_rows([
            {"updateMS": 50, "updateRenderOutcome": "rendered", "splitRenderOutcome": "rendered"},
            {"updateMS": 30000, "updateRenderOutcome": "cancelled", "splitRenderOutcome": "rendered"},
        ])
        self.assertEqual(summary["medians"], {"updateMS": 15025})
        self.assertEqual(summary["render_outcomes"]["updateRenderOutcome"],
                         {"cancelled": 1, "rendered": 1})
        self.assertEqual(summary["render_outcomes"]["splitRenderOutcome"], {"rendered": 2})
        self.assertEqual(summarize_rows([])["medians"], {})

    def test_each_run_uses_its_own_empty_baseline(self):
        mib = 1048576
        samples = []
        for baseline, delta in [(10, 2), (20, 4), (30, 3)]:
            samples.append({"variant": "candidate", "case": "Memory/full/tools", "rows": [
                {"label": "empty", "size": 0, "heap": baseline * mib, "rss": baseline * mib},
                {"label": "updated", "size": 5000, "heap": (baseline + delta) * mib,
                 "rss": (baseline + delta) * mib, "peakRSS": (baseline + delta + 5) * mib},
            ]})
        updated = next(row for row in summarize(samples) if row["phase"] == "updated")
        self.assertEqual(updated["metrics"]["heapMiBAboveEmpty"],
                         {"median": 3, "min": 2, "max": 4, "samples": 3})
        self.assertEqual(updated["metrics"]["peakRSSMiBAboveEmpty"]["median"], 8)

    def test_preview_updates_are_reduced_per_run_and_phases_stay_separate(self):
        samples = [{"variant": "baseline", "case": "Preview/compact", "rows": [
            {"label": "updated", "size": 200, "updates": [1, 100, 3]},
            {"label": "expanded-update", "size": 200, "updates": [9]},
        ]}, {"variant": "baseline", "case": "Preview/compact", "rows": [
            {"label": "updated", "size": 200, "updates": [5, 5, 99]},
        ]}]
        rows = {row["phase"]: row for row in summarize(samples)}
        self.assertEqual(rows["updated"]["metrics"]["updateMS"]["median"], 4)
        self.assertEqual(rows["updated"]["metrics"]["updateMS"]["samples"], 2)
        self.assertEqual(rows["expanded-update"]["metrics"]["updateMS"]["median"], 9)


if __name__ == "__main__":
    unittest.main()
