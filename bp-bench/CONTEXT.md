# bp-bench

Blueprint + agentic orchestration'in kod uretim kalitesini olcen benchmark.

## Amac

Soru: BLUEPRINT.yaml spec'leri ve multi-agent yapilar sonucu iyilestiriyor mu?

4 mode karsilastirmasi:
- `no_blueprint + single_agent`
- `no_blueprint + multi_agent`
- `with_blueprint + single_agent`
- `with_blueprint + multi_agent`

---

## bp-agent API

```python
from bp_agent.agent import Agent, AgentConfig, AgentResult

# Config
config = AgentConfig(
    provider="gemini",                    # "gemini" | "codex" | "opus"
    model="gemini-3-flash-preview",       # provider'a gore model adi
    max_iterations=10,                    # max tool call dongusu
    temperature=0.3,
    enable_subagents=False,               # True = spawn_worker/spawn_workers aktif
    worker_model=None,                    # worker icin farkli model (opsiyonel)
    worker_provider=None,                 # worker icin farkli provider
    worker_max_iterations=10,
)

# Kullanim
agent = Agent("bench-agent", config=config)
result: AgentResult = agent.execute("Build a REST API server")

result.success   # bool
result.output    # str — agent'in give_result ile verdigi cikti
result.task_id   # Optional[str]
result.trace     # Optional[dict] — tool call trace
```

### Araclar (builtins)

Agent'in erisimi olan araclar:
- `bash(command)` — shell komutu calistir
- `read_file(path)` — dosya oku
- `write_file(path, content)` — dosya yaz
- `list_dir(path)` — dizin listele
- `give_result(result)` — sonuc dondur ve dur

### Multi-agent

`enable_subagents=True` ile agent su ek araclara sahip olur:
- `spawn_worker(instruction, context?)` — tek worker baslat
- `spawn_workers(tasks)` — paralel worker'lar baslat (ThreadPoolExecutor)

Worker'lar bagimsiz Agent instance'lari. Kendi tool registry'leri var.

---

## BLUEPRINT.yaml Formati

```yaml
_meta:
  version: "1"
intent: Paketin tek cumlede ne yaptigi
api:
  - "public interface tanimlari"
  - "class Foo: method1(), method2()"
  - "behavior: X durumunda Y yapar"
dependencies:
  - path: ../other-package
```

`intent` = ne yapacak, `api` = nasil gorunecek (public contract), `dependencies` = bagimliliklar.

---

## Senaryo: Multi-Package API Server

3 Python paketi (sadece stdlib):

### store/
- `models.py`: `Item` dataclass — id(str), name(str), price(float), quantity(int)
- `repository.py`: `ItemRepository` Protocol — add, get, list_all, update, delete
- `memory.py`: `MemoryRepository(ItemRepository)` in-memory impl
- `get/update/delete` missing ID'de `KeyError`

### api/
- `server.py`: `http.server` ile REST
- `GET /items`, `GET /items/{id}`, `POST /items`, `PUT /items/{id}`, `DELETE /items/{id}`
- JSON request/response, dogru HTTP status kodlari

### cli/
- `main.py`: `argparse` ile komut satiri
- `list`, `add --name X --price Y`, `get ID`, `delete ID`

### Judge — Deterministik (21 check)

**Structure (7):** dosya varliklari + no third-party imports
**Functional (8):** import + CRUD + HTTP round-trip
**Spec (4):** Protocol, KeyError, 404, Content-Type
**Integration (2):** server+cli uctan uca

Tum check'ler Python kodu ile — LLM judge YOK.

---

## Maliyet

- Gemini Flash = ucretsiz tier
- `max_iterations: 30` per agent
- 4 run x ~30 tur = ~120 agent turn
- `--provider/--model` ile override

---

## Dosyalar

- `run_bench.py` — tek dosya benchmark runner
- `test_checks.py` — judge check'lerini pytest ile dogrula
- `CONTEXT.md` — bu dosya
