"""Pinned paper-spec backbone. CPU only; logits are ranking values."""
import torch
from torch import nn

PROFILE = "hearank-paper-spec-v1"
WIDTHS = (512, 512, 256)


class Ranker(nn.Module):
    def __init__(self, dimension):
        super().__init__()
        layers = []
        for width in WIDTHS:
            layers.extend((nn.Linear(dimension, width), nn.BatchNorm1d(width, eps=1e-5, momentum=0.1), nn.ReLU()))
            dimension = width
        layers.append(nn.Linear(dimension, 1))
        self.layers = nn.Sequential(*layers)

    def forward(self, x):
        return self.layers(x).squeeze(-1)

    def export(self):
        result = []
        for index in (0, 3, 6, 9):
            linear = self.layers[index]
            layer = {"weights": linear.weight.detach().tolist(), "bias": linear.bias.detach().tolist()}
            if index < 9:
                bn = self.layers[index + 1]
                layer["bn"] = {"mean": bn.running_mean.tolist(), "variance": bn.running_var.tolist(), "gamma": bn.weight.detach().tolist(), "beta": bn.bias.detach().tolist(), "eps": bn.eps}
            result.append(layer)
        return result
