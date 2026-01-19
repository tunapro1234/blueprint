# bp-cli Implementation Playbook

Bu repo `bp` (blueprint-cli) ile snapshot-based implementation kullanır.

## Aktif Plan: Yapısal Sadeleştirme

**Durum:** Planlama aşamasında

### Değişiklikler
1. **deps/ klasörü kaldırıldı** - Symlink'ler direkt pakette
2. **impl/ klasörü kaldırıldı** - Kod direkt snapshot'ta
3. **3 development mode** - pussy, ro, hide (default: ro)
4. **Default state_dir** - `.blueprint/` (gizli)

### Yeni Snapshot Yapısı
```
package/
├── BLUEPRINT.yaml
├── main.go
├── yamlparser/ → ../yamlparser/.blueprint/history/{pinned_id}/
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
| Mode | `bp impl` | `bp apply` | Dosyalar |
|------|-----------|------------|----------|
| **pussy** | impl.lock oluştur | impl.lock sil | Her zaman var, her zaman writable |
| **ro** (default) | impl.lock + chmod +w | impl.lock sil + chmod -w | Her zaman var, lock olmadan read-only |
| **hide** | impl.lock + restore | impl.lock sil + temizle | Sadece impl sırasında var |

```yaml
_meta:
  state_dir: ".blueprint"  # default
  mode: ro                  # default (pussy | ro | hide)
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
│  3. bp impl [clean | <snapshot_id>]                         │
│     └── ro/hide mode: dosyaları writable yapar/restore eder │
│     └── Symlink'ler oluşturulur (direkt pakette)            │
│     └── impl.lock açılır (implementing state)               │
│                           │                                  │
│                           ▼                                  │
│  4. IMPLEMENTATION (Kod Yazma)                              │
│     └── Working tree'de kodu düzenle                        │
│     └── Import'lar direkt symlink üzerinden                 │
│     └── Test yaz / güncelle                                 │
│                           │                                  │
│                           ▼                                  │
│  5. bp apply -m "message"                                   │
│     └── Validate + test çalıştır                            │
│     └── history/{id}/ altına kopyalar + symlink             │
│     └── ro mode: read-only yapar                            │
│     └── hide mode: dosyaları temizler                       │
│     └── impl.lock kapatılır                                 │
│                           │                                  │
│                           ▼                                  │
│  6. ITERATION (Sonraki değişiklik)                          │
│     └── bp impl → kod düzenle → bp apply                    │
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
- `.blueprint/impl.lock`: Aktif implementasyon kilidi
- `.blueprint/state.yaml`: Dependency pin'leri, staleness bilgisi

## Symlink Yapısı (Yeni)
```
package/
├── main.go
├── util.go
├── yamlparser/ → ../yamlparser/.blueprint/history/{pinned_id}/
└── commands/ → ../commands/.blueprint/history/{pinned_id}/
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

## Temel Komutlar

```bash
# 1. Blueprint yaz (manuel)
# 2. (Opsiyonel) değişenleri sırala (default recursive)
./bp plan .

# 3. Restore + implement başlat (symlink'ler oluşur)
./bp impl

# 4. Kodu yaz, test et (import'lar direkt symlink üzerinden)
cd src && go test ./...

# 5. Uygula + kapat
./bp apply -m "implement feature X"

# 6. Dependency güncelle (symlink güncellenir)
./bp upgrade --all

# 7. Sonraki iterasyon için
./bp impl
```

## Snapshot Davranışı

### `bp apply` çalıştırıldığında:
1. Blueprint validate edilir
2. `tests.verification` komutları çalıştırılır
3. Dosyalar `history/{id}/` altına kopyalanır (BLUEPRINT.yaml, kod, symlink'ler)
4. Mode'a göre:
   - **pussy**: impl.lock silinir
   - **ro**: impl.lock silinir, dosyalar read-only yapılır (chmod -w)
   - **hide**: impl.lock silinir, working tree'deki kod dosyaları silinir

### `bp implement` çalıştırıldığında:
- Mode'a göre:
  - **pussy**: impl.lock oluşturulur
  - **ro**: impl.lock oluşturulur, dosyalar writable yapılır (chmod +w)
  - **hide**: Current snapshot'tan dosyalar restore edilir, impl.lock oluşturulur
- Symlink'ler oluşturulur (direkt pakette)

### `bp upgrade` çalıştırıldığında:
1. Symlink'ler yeni pinned snapshot'a güncellenir
2. `tests.verification` çalıştırılır
3. Test başarısız olursa: rollback + rotten flag
4. Test başarılı olursa: state.yaml güncellenir

## Rotten Flag Sistemi
Bir dependency upgrade'ı sırasında testler fail ederse:
- Symlink eski haline döndürülür (rollback)
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
- `_meta.mode` ile ayarlanır (default: `ro`)
- **pussy**: Hiçbir şeyi zorlamaz
- **ro**: impl.lock yokken read-only zorlar (chmod)
- **hide**: apply sonrası kodları kaldırır

## Kurallar
1. Önce blueprint yaz, sonra implement et
2. Blueprint değişikliği olmadan yeni özellik ekleme
3. API imzalarını koru (breaking change için yeni versiyon)
4. Her snapshot için anlamlı mesaj yaz
5. `bp impl` ve `bp apply` ardışık/tek yönlü akış:
   - `bp impl` → çalış → `bp apply` ile kapat
   - `bp apply` olmadan ikinci `bp impl` çalışmaz
6. Import'lar direkt symlink'ler üzerinden yapılır
7. `bp upgrade` symlink'leri günceller, kod değişikliği gerekmez

## Agent Rehberi (Önerilen İş Akışı)
1. `bp plan .` ile leaf-first sıra çıkar (default recursive)
2. Her paket için:
   - `bp impl` (veya `bp impl clean`)
   - Değişiklikleri yap (import'lar direkt symlink üzerinden)
   - Testleri çalıştır
   - `bp apply -m "..."` ile snapshot al
3. Dependency güncellemek için:
   - `bp upgrade --all` (symlink'ler güncellenir)
4. `bp status .` ile genel kontrol (default recursive)

Notlar:
- BLUEPRINT dosyaları `bp apply` sonrası working tree'de kalır.
- ro mode'da impl.lock yokken dosyalar read-only'dir.
- Import'lar direkt symlink'ler üzerinden yapılır (deps/ yok).
- `bp upgrade` çalıştırıldığında symlink hedefleri güncellenir, kod değişikliği gerekmez.

## Komut Referansı

| Komut | Açıklama |
|-------|----------|
| `bp implement` | impl.lock aç + symlink oluştur (mode'a göre restore/chmod) |
| `bp apply -m "msg"` | Snapshot al + symlink + kapat (mode'a göre temizle/chmod) |
| `bp validate [--no-recursive]` | Blueprint doğrula |
| `bp status [--no-recursive]` | Değişiklik kontrolü |
| `bp plan [--no-recursive]` | Leaf-first uygulanacak paketleri listeler |
| `bp map [--no-recursive]` | Proje haritası: tüm paketler, snapshot'lar, dependency'ler |
| `bp upgrade [--all] [--force]` | Dependency güncelle (symlink günceller, test çalıştırır) |
| `bp cancel` | Aktif implementasyonu iptal et |
| `bp log [-n N]` | Snapshot history |
| `bp diff [id1] [id2]` | Snapshot karşılaştır |
| `bp show [id]` | Belirli snapshot'ı göster |
| `bp deps` | Dependency graph |
| `bp init` | Yeni BLUEPRINT.yaml oluştur |
