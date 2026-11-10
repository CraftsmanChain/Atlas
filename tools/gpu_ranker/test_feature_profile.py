"""Pure-Python policy tests; no optional ML runtime is required."""
import copy
import unittest
import feature_profile as fp
from preprocess import fit,transform,CATEGORIES,NUMERIC

class AvailabilityPolicyTests(unittest.TestCase):
    def test_only_exact_static_unknowns_are_optional(self):
        for name in ('host_type','gpu_model','manufacturer','main_board','quota_group','data_center','rack'):
            self.assertFalse(fp.blocked(['host_context_unknown:'+name]))
            row={'features':{'numeric':{n:0 for n in NUMERIC},'categorical':{n:'value' for n in CATEGORIES},'by_fault_type':{},'by_severity':{}}}
            row['features']['categorical'][name]=None
            self.assertTrue(transform(fit([row]),[row])[0])
        for b in ('history_coverage_unknown','episode_state_unknown','host_context_conflict:rack','host_context_unknown:new_field','new_unknown'):
            self.assertTrue(fp.blocked([b]))
    def test_manifest_is_closed_and_bound_to_schema(self):
        schema=[{'name':'fixture'}]
        d={'spec':{'profile':fp.PROFILE},'version':'gpu-host-history-dataset-v3','feature_contract':fp.FEATURE,'schema':schema,'availability_manifest':fp.manifest(schema),'profile_feature_complete':True}
        self.assertFalse(fp.valid_dataset(d), "a caller-defined schema is never authoritative")
        changed=copy.deepcopy(d);changed['availability_manifest']['optional_blockers'].append('history_coverage_unknown')
        self.assertFalse(fp.valid_dataset(changed))
        changed=copy.deepcopy(d);changed['schema'][0]['name']='different'
        self.assertFalse(fp.valid_dataset(changed))
