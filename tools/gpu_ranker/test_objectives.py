import math
import unittest
from unittest.mock import patch
import torch
from objectives import objective, MAX_PAIRS


class ObjectiveTests(unittest.TestCase):
    def tensor(self, values):
        return torch.tensor(values, dtype=torch.float64, requires_grad=True)

    def test_zero_and_hand_losses(self):
        for method in ('pointwise','listnet','pairwise'):
            r=objective(method,self.tensor([0,0]),torch.tensor([1,0]),[1,1])
            self.assertAlmostEqual(r['loss'].item(),math.log(2))
        z=self.tensor([2,0]);y=torch.tensor([1,0])
        expected=math.log1p(math.exp(2))-2*math.e/(math.e+1)
        self.assertAlmostEqual(objective('listnet',z,y,[1,1])['loss'].item(),expected)
        self.assertAlmostEqual(objective('pairwise',z,y,[1,1])['loss'].item(),math.log1p(math.exp(-2)))

    def test_row_mean_is_not_query_mean(self):
        z=self.tensor([0]*2+[2]*8);y=torch.zeros(10);q=[1]*2+[2]*8
        loss=objective('pointwise',z,y,q)['loss'].item()
        a,b=math.log(2),math.log1p(math.exp(2))
        self.assertAlmostEqual(loss,(2*a+8*b)/10)
        self.assertNotAlmostEqual(loss,(a+b)/2)

    def test_gradients_and_permutation(self):
        for method in ('pointwise','listnet','pairwise'):
            z=self.tensor([0,0]);r=objective(method,z,torch.tensor([1,0]),[1,1]);r['loss'].backward()
            self.assertLess(z.grad[0].item(),0);self.assertGreater(z.grad[1].item(),0)
            self.assertTrue(torch.isfinite(z.grad).all())
            a=objective(method,self.tensor([2,-1,0,3]),torch.tensor([1,0,1,0]),[1,1,2,2])['loss']
            b=objective(method,self.tensor([3,2,0,-1]),torch.tensor([0,1,1,0]),[2,1,2,1])['loss']
            self.assertAlmostEqual(a.item(),b.item())

    def test_query_isolation_and_undefined(self):
        r=objective('pairwise',self.tensor([5,-5]),torch.tensor([1,0]),[1,2])
        self.assertIsNone(r['loss']);self.assertEqual(r['reason'],'no_positive_negative_pairs');self.assertEqual(r['pairs'],0)
        for y in ([0,0],[1,1]):
            r=objective('pairwise',self.tensor([0,0]),torch.tensor(y),[1,1]);self.assertIsNone(r['loss'])
            self.assertAlmostEqual(objective('listnet',self.tensor([0,0]),torch.tensor(y),[1,1])['loss'].item(),math.log(2))
        r=objective('pairwise',self.tensor([2,0,100]),torch.tensor([1,0,0]),[1,1,2])
        self.assertEqual(r['queries_with_pairs'],1);self.assertEqual(r['queries_without_pairs'],1)
        self.assertAlmostEqual(r['loss'].item(),math.log1p(math.exp(-2)))

    def test_extreme_and_invalid_inputs(self):
        for method in ('pointwise','listnet','pairwise'):
            z=self.tensor([1e20,-1e20]);loss=objective(method,z,torch.tensor([0,1]),[1,1])['loss'];self.assertTrue(torch.isfinite(loss));loss.backward();self.assertTrue(torch.isfinite(z.grad).all())
        for method,z,y,q in [('bad',[0,0],[1,0],[1,1]),('pairwise',[],[],[]),('pointwise',[float('nan')],[0],[1]),('listnet',[0],[2],[1]),('pointwise',[0],[0],[True]),('pairwise',[0],[0],[1.0])]:
            with self.assertRaises(ValueError):objective(method,self.tensor(z),torch.tensor(y),q)
        with self.assertRaises(ValueError):objective('pointwise',torch.tensor([1]),torch.tensor([0]),[1])
        with self.assertRaises(ValueError):objective('pointwise',torch.zeros(1,1),torch.zeros(1),[1])

    def test_pair_budget_precedes_cartesian_and_loss(self):
        # 400*400 exceeds the global pair budget, before softplus receives pairs.
        self.assertLess(MAX_PAIRS,400*400)
        with patch('objectives.F.softplus',side_effect=AssertionError('must not allocate loss pairs')):
            with self.assertRaisesRegex(ValueError,'pair budget'):objective('pairwise',torch.zeros(800),torch.tensor([1]*400+[0]*400),[1]*800)
