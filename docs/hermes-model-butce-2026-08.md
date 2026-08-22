# Ox Alpha kapanırsa — model ve bütçe seçenekleri

> **TASLAK — ölçüm devam ediyor.** Claude tarafı sayıları 22 Ağu 23:00 itibarıyla
> ölçüldü (ada/server-main) ve **iş o sırada hâlâ sürüyordu** — canlı sayılar,
> yarın büyümüş görünecekler. Hermes tarafı hacim çıkarımı ve çapa testi devam
> ediyor; o bölümler dolunca bu not kalkacak. Teslim: 25 Ağu akşamı.

**Karar sorusu:** Ox Alpha (`x-preview-f-free`, OpenCode Free, anahtarsız bedava)
~27 Ağustos'ta kapanabilir. Kapanırsa Hermes filosu hangi modelle çalışır ve
bunun günlük bedeli nedir?

## Bugün ölçülen resim (22 Ağu)

İki ayrı maliyet var ve karıştırılmamalı:

| taraf | bugünkü bedel | ne için |
|---|---|---|
| Hermes (OpenRouter yedeği) | **1.05 USD** | Ox Alpha rate-limit yediğinde düşen ~%17'lik trafik |
| Claude (süpervizyon) | **36.3M birim** (yalnız probot-outreach) | 8 bedava ajanı yöneten Claude agent'ı |

Çağrıların %83'ü bedava kanaldan geçti (2164 Ox Alpha / 452 DeepSeek çağrısı),
yani **1.05 USD tüm işin değil, yalnız taşan kısmın bedeli** — Ox Alpha
kapanırsa çarpan 1x değildir.

### Süpervizyonun şekli (ada ölçümü, 22 Ağu 23:00)

probot-outreach: 1042 tur, 36.3M birim, bunun **%73'ü cache_read** — üretim
değil, bağlam yeniden-okuma. Tur başına ~254k bağlam, ~929 token çıktı.
Kısa-tur ("sadece koordinasyon") payı yalnızca %17 — süpervizor gerçek iş
yapıyor.

> **Ölçümün ana bulgusu:** pahalı olan turun içeriği değil, **var olması**.
> Her tur ~254k bağlamı yeniden okuyor; tur uzun da olsa kısa da olsa bu bedel
> aynı. Bu yüzden "hangi model" sorusu tek başına maliyeti çözmez.

## Seçenekler

Dört yol var; ilk üçü "Hermes hangi modelle koşar", dördüncüsü ötekilerden
**bağımsız** ve muhtemelen en büyük kaldıraç.

### 1. DeepSeek V4 Flash birincil (bugünkü yedek, öne alınır)
- Günlük maliyet: _ölçüm bekliyor_
- Bozulan: _ölçüm bekliyor (araç çağırma davranışı, bağlam limiti)_

### 2. Başka bedava/ucuz kanal
- Adaylar ve fiyatları: _ölçüm bekliyor_
- Bozulan: bedava kanallar rate-limit ve bağlam sınırı getirir; Ox Alpha'nın
  bugün yediği limitler zaten fallback'i tetikliyordu.

### 3. Claude subagent'a dönüş (Hermes'i bırakmak)
- Maliyet tabanı: bugünkü 36.3M birim **ALT SINIR** olarak alınmalı — bu rakam
  süpervizyonun yanında işin bir kısmını da içeriyor; Hermes olmasaydı IG/video
  işi de üstüne binerdi (ada uyarısı).
- Bozulan: filo kotası (limit baskısı zaten var), eşzamanlılık.

### 4. Model değiştirmeden süpervizyon maliyetini düşürmek
Maliyetin %73'ü bağlam yeniden-okuma olduğuna göre iki kaldıraç:
- **Tur başına bağlam**: compact sıklığı — 254k steady-state mi, gün içinde
  büyüyüp compact ile mi düşüyor? _(ada'dan bağlam büyüme eğrisi istendi)_
- **Tur sayısı**: turların kaçı dışarıdan gelen mesajla başlıyor? Teslimatları
  batch'lemek süpervizor turunu azaltıyorsa iş bp tarafında. _(ada'dan kırılım
  istendi)_

## Ayrı anahtar meselesi (Tuna "sonra" dedi, kapanmadı)

Kullanılan OpenRouter anahtarı **kitap projesinin** (`/srv/kitap/.env`, aylık 20
USD). İki yönlü risk: tavan dolarsa yalnız Hermes değil **kitap tarafı da
durur** (server-main tespiti). Ayrı anahtar bu bağı keser.

## Kalan belirsizlikler

- Ox Alpha'nın kapanma tarihi kesin değil ("~27 Ağu" söylenti).
- Bugünün hacmi tipik bir gün mü? Tek günlük ölçüm; filo Hermes'i yeni kullanıyor.
