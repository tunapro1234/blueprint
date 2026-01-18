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
│     └── impl.lock açılır (implementing state)               │
│                           │                                  │
│                           ▼                                  │
│  4. IMPLEMENTATION (Kod Yazma)                              │
│     └── Working tree'de kodu düzenle                        │
│     └── API/spec'e uy                                       │
│     └── Test yaz / güncelle                                 │
│                           │                                  │
│                           ▼                                  │
│  5. bp ss -m "message"                                      │
│     └── Validate + test çalıştır                            │
│     └── history/{snapshot_id}/impl/ altına kopyalar         │
│     └── impl dosyalarını working tree'den temizler          │
│     └── impl.hidden yazar, impl.lock kapatılır              │
│                           │                                  │
│                           ▼                                  │
│  6. ITERATION (Sonraki değişiklik)                          │
│     └── bp impl → kod düzenle → bp ss                       │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Dosya Yapısı
- `BLUEPRINT.yaml`: intent, API, dependencies
- `BLUEPRINT.spec.yaml`: structure (impl dosyaları), tests
- `blueprint/history/{id}/impl/`: Snapshot'lanmış kod katmanları
- `blueprint/current`: Aktif snapshot ID
- `blueprint/impl.lock`: Aktif implementasyon kilidi
- `blueprint/impl.hidden`: Working tree gizli işareti

## Temel Komutlar

```bash
# 1. Blueprint yaz (manuel)
# 2. (Opsiyonel) değişenleri sırala
./bp plan -r .

# 3. Restore + implement başlat
./bp impl

# 4. Kodu yaz, test et
cd src && go test ./...

# 5. Snapshot al
./bp ss -m "implement feature X"

# 6. Sonraki iterasyon için restore
./bp impl
```

## Snapshot Davranışı
- `bp ss` çalıştırıldığında:
  1. Blueprint validate edilir
  2. `tests.verification` komutları çalıştırılır
  3. Impl dosyaları `history/{id}/impl/` altına kopyalanır
  4. Working tree'deki impl dosyaları **silinir**
  5. `impl.hidden` yazılır, `impl.lock` silinir
- `bp implement` çalıştırıldığında:
  - Current snapshot'tan dosyalar restore edilir
  - `impl.hidden` temizlenir, `impl.lock` açılır

## State Directory
- `_meta.state_dir` ile ayarlanır (default: `.blueprint/`)
- Bu repo `blueprint/` kullanır
- İçerik: `current`, `state.yaml`, `history/`

## Kurallar
1. Önce blueprint yaz, sonra implement et
2. Blueprint değişikliği olmadan yeni özellik ekleme
3. API imzalarını koru (breaking change için yeni versiyon)
4. Her snapshot için anlamlı mesaj yaz
5. `bp impl` ve `bp ss` ardışık/tek yönlü akış:  
   - `bp impl` → çalış → `bp ss` ile kapat
   - `bp ss` olmadan ikinci `bp impl` çalışmaz
6. `bp ss` sonrası working tree temizlenir; kod görmek için `bp impl` gerekir
7. Import/build için snapshot kopyaları tercih edilir (history/{id}/impl)

## Agent Rehberi (Önerilen İş Akışı)
1. `bp plan -r .` ile leaf-first sıra çıkar
2. Her paket için:
   - `bp impl` (veya `bp impl clean`)
   - Değişiklikleri yap
   - Testleri çalıştır
   - `bp ss -m "..."` ile snapshot al
3. `bp status -r .` ile genel kontrol

Notlar:
- BLUEPRINT dosyaları `bp ss` sonrası working tree'de kalır.
- `impl.hidden` varken `status` fresh kalabilir; kodu görmek için `bp impl` kullanılır.

## Komut Referansı

| Komut | Açıklama |
|-------|----------|
| `bp implement` | Current snapshot'tan dosyaları restore et |
| `bp ss -m "msg"` | Snapshot al (validate + test + save) |
| `bp validate [-r]` | Blueprint doğrula |
| `bp status [-r]` | Değişiklik kontrolü |
| `bp plan [-r]` | Leaf-first uygulanacak paketleri listeler |
| `bp cancel` | Aktif implementasyonu iptal eder (impl.lock temizler) |
| `bp log [-n N]` | Snapshot history |
| `bp diff [id1] [id2]` | Snapshot karşılaştır |
| `bp show [id]` | Belirli snapshot'ı göster |
| `bp deps` | Dependency graph |
