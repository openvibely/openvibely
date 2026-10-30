import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('reclamation', Path(__file__).with_name('reclamation.py'))
reclamation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reclamation)


class ReclamationAccountingTests(unittest.TestCase):
    def fixture(self, root):
        d = Path(root) / 'text-only-large-1-branch-1'
        d.mkdir()
        (d/'storage.json').write_text('{}')
        requests = []
        turns = []
        for turn in range(6):
            start = 1_000_000_000 * (turn+1)
            requests.append(dict(turn=turn, prior_turn_seen=True, first_text_delta_unix_ns=start+200_000_000, last_text_delta_unix_ns=start+900_000_000))
            # Reclamation exceeds allocation: old objects are allowed to die.
            samples = [dict(MS=ms, Objects=i*6, Frees=i*8, LiveObjects=100-i*2, Alloc=i*60, FreedBytes=i*80, Heap=1000-i*20) for i,ms in enumerate([0,100,300,500,800,950,1000])]
            (d/f'samples-{turn}.json').write_text(json.dumps(samples))
            turns.append(dict(turn=turn, wave_start_unix_ns=start, alloc_count=36, free_count=48, start_live_objects=100, end_live_objects=88, start_heap=1000, end_heap=880))
        (d/'turns.json').write_text(json.dumps(turns))
        (d/'model.json').write_text(json.dumps(dict(max_active=1, requests=requests)))
        return d

    def test_older_frees_and_boundary_intervals_are_not_lost(self):
        with tempfile.TemporaryDirectory() as root:
            self.fixture(root)
            rows, phases = reclamation.analyze(root)
            self.assertEqual(len(rows),6)
            self.assertTrue(all(r['net_live_objects']==-12 for r in rows))
            for turn in range(6):
                ps = [p for p in phases if p['turn']==turn]
                self.assertEqual(sum(p['Objects'] for p in ps),36)
                self.assertEqual(sum(p['Frees'] for p in ps),48)
                self.assertGreater(next(p['Objects'] for p in ps if p['phase']=='boundary-intervals'),0)

    def test_inconsistent_live_count_is_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            d = self.fixture(root)
            p = d/'samples-0.json'
            data = json.loads(p.read_text())
            data[1]['LiveObjects'] += 1
            p.write_text(json.dumps(data))
            with self.assertRaises(AssertionError):
                reclamation.analyze(root)

    def test_profiled_and_traced_runs_are_rejected(self):
        for marker in ['allocation-diagnostic.json','runtime-0.trace']:
            with self.subTest(marker=marker), tempfile.TemporaryDirectory() as root:
                d = self.fixture(root)
                (d/marker).write_text('{}')
                with self.assertRaises(ValueError):
                    reclamation.analyze(root)


if __name__=='__main__': unittest.main()
