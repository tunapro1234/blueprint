# bp-cli Implementation Playbook

Bu repo `bp` (blueprint-cli) ile snapshot-based implementation kullanır.

## Aktif Plan: Yapısal Sadeleştirme

**Durum:** Planlama aşamasında

### Değişiklikler
1. **deps/ klasörü kaldırıldı** - Symlink'ler sadece snapshot history içinde
2. **impl/ klasörü kaldırıldı** - Kod direkt snapshot'ta
3. **3 development mode** - meek, ro, hide (default: meek)
4. **Default state_dir** - `.blueprint/` (gizli)

### Yeni Snapshot Yapısı
```
package/
├── BLUEPRINT.yaml
├── main.go
├── yamlparser/                      # gerçek dependency klasörü
├── .blueprint/
│   ├── state.yaml
│   ├── current
│   └── history/
│       └── {snapshot_id}/
│           ├── BLUEPRINT.yaml
│           ├── meta.yaml
│           ├── main.go
│           └── yamlparser/ → ../../yamlparser/.blueprint/history/{pinned_id}/
```

### Development Modes
| Mode | `bp impl` | `bp ss` | Dosyalar |
|------|-----------|---------|----------|
| **meek** (default) | busy flag only | snapshot only | Her zaman var, her zaman writable |
| **ro** | derleme boyunca writable, sonra read-only | izin değiştirmez | Her zaman var, idle'da read-only |
| **hide** | derleme için görünür, sonra gizle | snapshot alır (kod yoksa boş) | Sadece derleme sırasında var |

```yaml
_meta:
  state_dir: ".blueprint"  # default
  mode: meek               # default (meek | ro | hide)
```

---

## Blueprint Akışı

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│  1. PLAN (Tasarım Aşaması)                                  │
│     └── BLUEPRINT.yaml yaz: intent, api, dependencies       │
│     └── BLUEPRINT.spec.yaml yaz: structure (dosya listesi)  │
│                           │                                  │
│                           ▼                                  │
│  2. bp plan (default recursive)                             │
│     └── Leaf-first sırada değişen paketleri listeler        │
│                           │                                  │
│                           ▼                                  │
│  3. bp impl [--no-snapshot|-ns]                             │
│     └── Agentic derleme + opsiyonel snapshot                │
│     └── impl.lock sadece derleme sırasında "busy" flag      │
│                           │                                  │
│                           ▼                                  │
│  4. MANUAL IMPLEMENTATION (Opsiyonel)                       │
│     └── Working tree her zaman aktif                        │
│     └── Kod düzenle, test et                                │
│                           │                                  │
│                           ▼                                  │
│  5. bp ss -m "message"                                      │
│     └── Validate + test çalıştır                            │
│     └── history/{id}/ altına kopyalar + symlink             │
│     └── snapshot read+execute yapılır                       │
│                           │                                  │
│                           ▼                                  │
│  6. ITERATION (Sonraki değişiklik)                          │
│     └── bp impl veya bp ss                                  │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Root Blueprint
- `_meta.root: true` ile proje root'u işaretlenir
- Alt paketlerde argümansız komut → root bulunur, recursive çalıştırılır
- Explicit path verilirse (`.`, `./sub`) bulunduğun yerden recursive çalışır
- `--no-recursive` verilirse sadece tek paket çalışır
- Root bulunamazsa en üstteki blueprint root sayılır

```bash
# yamlparser/ içindeyken
bp validate                 # → root bul, tüm proje için recursive
bp validate .               # → sadece bu paket (recursive)
bp validate . --no-recursive  # → sadece bu paket (non-recursive)
```

## Dosya Yapısı
- `BLUEPRINT.yaml`: intent, API, dependencies (root'ta `_meta.root: true`)
- `BLUEPRINT.spec.yaml`: structure (impl dosyaları), tests
- `.blueprint/history/{id}/`: Snapshot'lanmış kod + symlink'ler
- `.blueprint/current`: Aktif snapshot ID
- `.blueprint/impl.lock`: Derleme sırasında busy flag
- `.blueprint/state.yaml`: Dependency pin'leri, staleness bilgisi

## Symlink Yapısı (Yeni)
```
package/
├── main.go
├── util.go
├── yamlparser/         # gerçek dependency klasörü
└── commands/           # gerçek dependency klasörü
```

Snapshot içinde:
```
.blueprint/history/{id}/
├── BLUEPRINT.yaml
├── meta.yaml
├── main.go
├── util.go
├── yamlparser/ → ../../yamlparser/.blueprint/history/{pinned_id}/
└── commands/ → ../../commands/.blueprint/history/{pinned_id}/
```

## Multi-language Kökler
- Dil kökleri `src-<lang>` altında tutulur (örn. `src-go`, `src-rs`)
- `bp new-lang rs --from go` yeni kök oluşturur
  - Paket klasörleri oluşturulur
  - API blueprint dosyaları symlink edilir, diğer blueprint dosyaları kopyalanır
  - Implementasyon dosyaları kopyalanmaz
- `bp map --langs` API hash eşitliğini gösterir

## Temel Komutlar

```bash
# 1. Blueprint yaz (manuel)
# 2. (Opsiyonel) değişenleri sırala (default recursive)
./bp plan .

# 3. Agentic derleme + snapshot (opsiyonel)
./bp impl

# 4. Kodu yaz, test et (import'lar gerçek dependency klasörlerinden)
cd src && go test ./...

# 5. Manuel snapshot
./bp ss -m "implement feature X"

# 6. Dependency güncelle (pin güncellenir; symlink snapshot'ta oluşur)
./bp upgrade --all

# 6.5 Yeni dil kökü oluştur (opsiyonel)
./bp new-lang rs --from go

# 7. Sonraki iterasyon için
./bp impl veya ./bp ss
```

## Snapshot Davranışı

### `bp ss` çalıştırıldığında:
1. Blueprint validate edilir
2. `tests.verification` komutları çalıştırılır
3. Dosyalar `history/{id}/` altına kopyalanır (BLUEPRINT.yaml, kod, symlink'ler)
4. Snapshot altındaki tüm dosyalar read+execute yapılır (chmod 0555, symlink hariç)
5. Working tree'ye dokunulmaz

### `bp impl` çalıştırıldığında:
- Agentic derleme yapılır, opsiyonel snapshot alınır (`--no-snapshot` ile kapatılır)
- Mode'a göre idle davranış:
  - **meek**: hiçbir şey yapma
  - **ro**: dosyaları read-only yap
  - **hide**: kod dosyalarını gizle
- impl.lock sadece derleme süresince "busy" flag'dir

### `bp upgrade` çalıştırıldığında:
1. state.yaml pinned/latest bilgileri güncellenir
2. `tests.verification` çalıştırılır
3. Test başarısız olursa: rollback + rotten flag
4. Test başarılı olursa: state.yaml güncellenir

## Rotten Flag Sistemi
Bir dependency upgrade'ı sırasında testler fail ederse:
- Pinned state eski halinde kalır (rollback)
- Dependency'nin `meta.yaml` dosyasına `rotten: true` yazılır
- Consumer'ın `state.yaml` deps bölümüne `rotten: true` eklenir

Rotten dependency uyarısı:
- **Tüm bp komutları** başlangıçta rotten dependency kontrolü yapar
- Rotten varsa uyarı gösterir: `⚠ Rotten dependency: {dep} (upgrade failed)`
- `bp upgrade` rotten dependency'leri atlar (--force ile zorlanabilir)

## Import Kullanımı (Yeni)
```python
# Python - direkt import
import yamlparser

# Go - go.mod'da replace direktifi
replace proj/yamlparser => ./yamlparser

# TypeScript - tsconfig paths
import { parser } from "yamlparser"
```

**Eski (deps/ ile):**
```python
from deps.yamlparser import parser  # KALDIRILDI
```

## State Directory
- `_meta.state_dir` ile ayarlanır (default: `.blueprint/`)
- İçerik: `current`, `state.yaml`, `history/`

## Development Mode
- `_meta.mode` ile ayarlanır (default: `meek`)
- **meek**: Hiçbir şeyi zorlamaz
- **ro**: idle durumda read-only zorlar (chmod)
- **hide**: derleme sonrası kodları gizler

## Kurallar
1. Önce blueprint yaz, sonra implement et
2. Blueprint değişikliği olmadan yeni özellik ekleme
3. API imzalarını koru (breaking change için yeni versiyon)
4. Her snapshot için anlamlı mesaj yaz
5. `bp impl` derleme + opsiyonel snapshot yapar (`--no-snapshot` hariç)
6. Manuel değişiklikler için `bp ss` kullan
7. Import'lar gerçek dependency klasörlerinden yapılır
8. `bp upgrade` sadece pinleri günceller (symlink snapshot'ta oluşur)

## Agent Rehberi (Önerilen İş Akışı)
1. `bp plan .` ile leaf-first sıra çıkar (default recursive)
2. Her paket için:
   - `bp impl` (agentic) veya manuel düzenle
   - Değişiklikleri yap (import'lar gerçek dependency klasörlerinden)
   - Testleri çalıştır
   - `bp ss -m "..."` ile snapshot al
3. Dependency güncellemek için:
   - `bp upgrade --all` (pinler güncellenir)
4. `bp status .` ile genel kontrol (default recursive)

Notlar:
- BLUEPRINT dosyaları snapshot sonrası working tree'de kalır.
- ro mode'da idle durumda dosyalar read-only'dir.
- Import'lar gerçek dependency klasörlerinden yapılır (deps/ yok).
- `bp upgrade` çalıştırıldığında pinler güncellenir, symlink'ler snapshot'ta oluşur.
- Snapshot altındaki tüm dosyalar read+execute yapılır (symlink hariç).

## Komut Referansı

| Komut | Açıklama |
|-------|----------|
| `bp implement` | Agentic derleme + opsiyonel snapshot |
| `bp ss -m "msg"` | Manuel snapshot (validate + test + read+execute) |
| `bp new-lang <lang>` | Yeni dil kökü oluştur (API blueprint symlink, diğerleri kopya) |
| `bp validate [--no-recursive]` | Blueprint doğrula |
| `bp status [--no-recursive]` | Değişiklik kontrolü |
| `bp plan [--no-recursive]` | Leaf-first uygulanacak paketleri listeler |
| `bp map [--no-recursive] [--langs]` | Proje haritası + dil karşılaştırması |
| `bp upgrade [--all] [--force]` | Dependency güncelle (pin günceller, test çalıştırır) |
| `bp cancel` | Aktif derlemeyi iptal et |
| `bp log [-n N]` | Snapshot history |
| `bp diff [id1] [id2]` | Snapshot karşılaştır |
| `bp show [id]` | Belirli snapshot'ı göster |
| `bp deps` | Dependency graph |
| `bp init` | Yeni BLUEPRINT.yaml oluştur |
