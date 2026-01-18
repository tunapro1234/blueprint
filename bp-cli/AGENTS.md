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
│  5. bp apply -m "message"                                   │
│     └── (impl mode) Validate + test çalıştır                │
│     └── history/{snapshot_id}/impl/ altına kopyalar         │
│     └── impl dosyalarını working tree'den temizler          │
│     └── impl.hidden yazar, impl.lock kapatılır              │
│                           │                                  │
│                           ▼                                  │
│  6. ITERATION (Sonraki değişiklik)                          │
│     └── bp impl → kod düzenle → bp ss                       │
│                                                             │
│  (IMPORT PATCH AKIŞI)                                       │
│  3b. bp patch [<snapshot_id>]                               │
│     └── Snapshot'tan working tree'yi restore eder           │
│     └── impl.lock açılır (mode=patch)                       │
│  4b. PATCH (Import/include güncelle)                        │
│     └── Sadece import/include yollarını değiştir            │
│  5b. bp apply                                                │
│     └── Değişiklikleri snapshot impl içine geri yazar       │
│     └── impl dosyalarını working tree'den temizler          │
│     └── impl.hidden yazar, impl.lock kapatılır              │
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

# 5. Uygula + kapat (default)
./bp apply -m "implement feature X"

# 5b. (Opsiyonel) Import patch akışı
./bp patch
# import/include değişiklikleri
./bp apply

# 6. Sonraki iterasyon için restore
./bp impl
```

## Snapshot Davranışı
- `bp ss` çalıştırıldığında (legacy, tercih edilmez):
  1. Blueprint validate edilir
  2. `tests.verification` komutları çalıştırılır
  3. Impl dosyaları `history/{id}/impl/` altına kopyalanır
  4. Working tree'deki impl dosyaları **silinir**
  5. `impl.hidden` yazılır, `impl.lock` silinir
- `bp apply` çalıştırıldığında (impl mode):
  - `bp ss` ile aynı işi yapar (snapshot alır + temizler)
- `bp implement` çalıştırıldığında:
  - Current snapshot'tan dosyalar restore edilir
  - `impl.hidden` temizlenir, `impl.lock` açılır

## Patch Davranışı
- `bp patch` çalıştırıldığında:
  - Current snapshot'tan dosyalar restore edilir (veya verilen snapshot)
  - `impl.hidden` temizlenir, `impl.lock` açılır (mode=patch)
- `bp apply` çalıştırıldığında:
  - Import/include değişiklikleri snapshot impl içine geri yazılır
  - Dependency pin'leri (state.yaml) güncellenir
  - Yeni snapshot oluşmaz; mevcut snapshot mutasyona uğrar
  - Working tree temizlenir, `impl.hidden` yazılır, `impl.lock` silinir

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
6. `bp ss` sonrası working tree temizlenir; kod görmek için `bp impl` gerekir
7. Import/build için snapshot kopyaları tercih edilir (history/{id}/impl); working tree'den import yapılmaz
8. Dependency snapshot güncellendiğinde dependents içindeki import yolları **güncellenmelidir**
9. Sadece import yolu güncellemesi yapıldıysa **yeni snapshot alınmaz**; `bp patch`/`bp apply` ile mevcut snapshot güncellenir

## Agent Rehberi (Önerilen İş Akışı)
1. `bp plan -r .` ile leaf-first sıra çıkar
2. Her paket için:
   - `bp impl` (veya `bp impl clean`)
   - Değişiklikleri yap
   - Testleri çalıştır
   - `bp ss -m "..."` ile snapshot al
3. Sadece import yollarını güncellemek gerekiyorsa:
   - `bp patch`
   - Import/include güncelle
   - `bp apply`
4. `bp status -r .` ile genel kontrol

Notlar:
- BLUEPRINT dosyaları `bp ss` sonrası working tree'de kalır.
- `impl.hidden` varken `status` fresh kalabilir; kodu görmek için `bp impl` kullanılır.
- Importlar **daima** snapshot path'lerine referans verir (working tree değil).
- Plan sırasında dependency update varsa, ilgili paketlerdeki import yollarını güncelle; tek değişiklik buysa `bp patch`/`bp apply` kullan.

## Komut Referansı

| Komut | Açıklama |
|-------|----------|
| `bp implement` | Current snapshot'tan dosyaları restore et |
| `bp apply -m "msg"` | (impl mode) Snapshot al (validate + test + save) |
| `bp ss -m "msg"` | Legacy snapshot (apply yerine) |
| `bp validate [-r]` | Blueprint doğrula |
| `bp status [-r]` | Değişiklik kontrolü |
| `bp plan [-r]` | Leaf-first uygulanacak paketleri listeler |
| `bp patch` | Import/include güncelleme oturumu başlatır |
| `bp apply` | Patch oturumunu kapatır, snapshot impl'i günceller |
| `bp cancel` | Aktif implementasyon/patch oturumunu iptal eder (impl.lock temizler) |
| `bp log [-n N]` | Snapshot history |
| `bp diff [id1] [id2]` | Snapshot karşılaştır |
| `bp show [id]` | Belirli snapshot'ı göster |
| `bp deps` | Dependency graph |
