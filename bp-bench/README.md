# bp-bench

> For the developer's vision and thoughts behind this project, see [HUMANS.md](../HUMANS.md).

Benchmark suite measuring the effect of Blueprint specs and multi-agent orchestration on code generation quality.

## Question

Does BLUEPRINT.yaml + multi-agent structure improve output compared to vanilla single-agent?

## Modes

| Mode | Blueprint | Agents |
|------|-----------|--------|
| Baseline | No | Single |
| Multi-only | No | Multi |
| Blueprint-only | Yes | Single |
| Full | Yes | Multi |

## Usage

```bash
python run_bench.py
python run_full_bench.py
python test_checks.py
```

## License

GPL-3.0
