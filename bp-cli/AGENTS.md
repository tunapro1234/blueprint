# bp-cli Implementation Playbook

Bu repo `bp` (blueprint-cli) ile snapshot-based implementation kullanır.

## Blueprint Akışı

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│  1. PLAN (Tasarım Aşaması)                                  │
│     └── BLUEPRINT.yaml yaz: intent, api, dependencies       │
│     └── BLUEPRINT.spec.yaml yaz: structure (dosya listesi)  │
│                           │                                  │
│                           ▼                                  │
│  2. bp plan -r (opsiyonel)                                  │
│     └── Leaf-first sırada değişen paketleri listeler        │
│                           │                                  │
│                           ▼                                  │
│  3. bp impl [clean | <snapshot_id>]                         │
│     └── Snapshot'tan working tree'yi restore eder           │
│     └── deps/ klasörüne symlink'ler oluşturur               │
│     └── impl.lock açılır (implementing state)               │
│                           │                                  │
│                           ▼                                  │
│  4. IMPLEMENTATION (Kod Yazma)                              │
│     └── Working tree'de kodu düzenle                        │
│     └── Import'lar deps/ üzerinden yapılır                  │
│     └── Test yaz / güncelle                                 │
│                           │                                  │
│                           ▼                                  │
│  5. bp apply -m "message"                                   │
│     └── Validate + test çalıştır                            │
│     └── history/{id}/impl/ altına kopyalar + deps/ symlink  │
│     └── impl dosyalarını working tree'den temizler          │
│     └── impl.hidden yazar, impl.lock kapatılır              │
│                           │                                  │
│                           ▼                                  │
│  6. ITERATION (Sonraki değişiklik)                          │
│     └── bp impl → kod düzenle → bp apply                    │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Dosya Yapısı
- `BLUEPRINT.yaml`: intent, API, dependencies
- `BLUEPRINT.spec.yaml`: structure (impl dosyaları), tests
- `blueprint/history/{id}/impl/`: Snapshot'lanmış kod + deps/ symlink'leri
- `blueprint/current`: Aktif snapshot ID
- `blueprint/impl.lock`: Aktif implementasyon kilidi
- `blueprint/impl.hidden`: Working tree gizli işareti

## Symlink Yapısı
```
impl/
├── main.go
├── util.go
└── deps/
    ├── yamlparser/ → ../../../yamlparser/blueprint/history/{pinned_id}/impl/
    └── commands/ → ../../../commands/blueprint/history/{pinned_id}/impl/
```

## Temel Komutlar

```bash
# 1. Blueprint yaz (manuel)
# 2. (Opsiyonel) değişenleri sırala
./bp plan -r .

# 3. Restore + implement başlat (deps/ symlink'leri oluşur)
./bp impl

# 4. Kodu yaz, test et (import'lar deps/ üzerinden)
cd src && go test ./...

# 5. Uygula + kapat
./bp apply -m "implement feature X"

# 6. Dependency güncelle (symlink güncellenir)
./bp upgrade --all

# 7. Sonraki iterasyon için restore
./bp impl
```

## Snapshot Davranışı
- `bp apply` çalıştırıldığında:
  1. Blueprint validate edilir
  2. `tests.verification` komutları çalıştırılır
  3. Impl dosyaları `history/{id}/impl/` altına kopyalanır
  4. `deps/` klasörüne symlink'ler oluşturulur
  5. Working tree'deki impl dosyaları **silinir**
  6. `impl.hidden` yazılır, `impl.lock` silinir
- `bp implement` çalıştırıldığında:
  - Current snapshot'tan dosyalar restore edilir
  - `deps/` klasörüne symlink'ler oluşturulur
  - `impl.hidden` temizlenir, `impl.lock` açılır
- `bp upgrade` çalıştırıldığında:
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

## Import Kullanımı
```python
# Python
from deps.yamlparser import parser

# Go - go.mod'da replace direktifi
replace proj/yamlparser => ./deps/yamlparser

# TypeScript - tsconfig paths
import { parser } from "@deps/yamlparser"
```

## State Directory
- `_meta.state_dir` ile ayarlanır (default: `.blueprint/`)
- Bu repo `blueprint/` kullanır
- İçerik: `current`, `state.yaml`, `history/`

## Kurallar
1. Önce blueprint yaz, sonra implement et
2. Blueprint değişikliği olmadan yeni özellik ekleme
3. API imzalarını koru (breaking change için yeni versiyon)
4. Her snapshot için anlamlı mesaj yaz
5. `bp impl` ve `bp apply` ardışık/tek yönlü akış:
   - `bp impl` → çalış → `bp apply` ile kapat
   - `bp apply` olmadan ikinci `bp impl` çalışmaz
6. `bp apply` sonrası working tree temizlenir; kod görmek için `bp impl` gerekir
7. Import'lar deps/ klasöründeki symlink'ler üzerinden yapılır
8. `bp upgrade` symlink'leri günceller, kod değişikliği gerekmez

## Agent Rehberi (Önerilen İş Akışı)
1. `bp plan -r .` ile leaf-first sıra çıkar
2. Her paket için:
   - `bp impl` (veya `bp impl clean`)
   - Değişiklikleri yap (import'lar deps/ üzerinden)
   - Testleri çalıştır
   - `bp apply -m "..."` ile snapshot al
3. Dependency güncellemek için:
   - `bp upgrade --all` (symlink'ler güncellenir)
4. `bp status -r .` ile genel kontrol

Notlar:
- BLUEPRINT dosyaları `bp apply` sonrası working tree'de kalır.
- `impl.hidden` varken `status` fresh kalabilir; kodu görmek için `bp impl` kullanılır.
- Import'lar deps/ klasöründeki symlink'ler üzerinden yapılır.
- `bp upgrade` çalıştırıldığında symlink hedefleri güncellenir, kod değişikliği gerekmez.

## Komut Referansı

| Komut | Açıklama |
|-------|----------|
| `bp implement` | Snapshot restore + deps/ symlink oluştur |
| `bp apply -m "msg"` | Snapshot al + deps/ symlink + temizle |
| `bp validate [-r]` | Blueprint doğrula |
| `bp status [-r]` | Değişiklik kontrolü |
| `bp plan [-r]` | Leaf-first uygulanacak paketleri listeler |
| `bp map [-r]` | Proje haritası: tüm paketler, snapshot'lar, dependency'ler |
| `bp upgrade [--all] [--force]` | Dependency güncelle (symlink günceller, test çalıştırır) |
| `bp cancel` | Aktif implementasyonu iptal et |
| `bp log [-n N]` | Snapshot history |
| `bp diff [id1] [id2]` | Snapshot karşılaştır |
| `bp show [id]` | Belirli snapshot'ı göster |
| `bp deps` | Dependency graph |
| `bp init` | Yeni BLUEPRINT.yaml oluştur |
