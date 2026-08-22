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
Maliyetin %73'ü bağlam yeniden-okuma olduğuna göre iki kaldıraç ölçüldü
(ada, 23 Ağu 00:00). İkisinin de **tavanı** var ve ikisi de ilk üç seçenekten
bağımsız uygulanabilir.

**4a. Bağlam (compact) — tavan ~%11.** Bağlam steady-state değil: gün boyu
monoton büyüyor, sadece 3 kez sıfırlanıyor (17:57, 21:02, 23:52) ve **605k
zirve** yapıyor — resume eşiğimizin iki katı. Önceki "254k" gün ortalamasıydı,
tavan değil (ada düzeltmesi). Bağlam hiç 300k'yı aşmasaydı okunan token 41M
azalırdı ≈ bugünkü 36.3M'in %11'i. Bu bir **üst sınır**: compact'in kendi
maliyeti, özet kaybı ve sonrasında cache'in yeniden yazılması düşülmeli.

> **bp tarafında kusur (blueprint, doğrulandı):** `bp compact` politikası
> **24 saat boşta + 200k bağlam** istiyor. Gün boyu çalışan bir agent bu
> filtreden hiç geçmez — oysa pahalı olan tam da odur. probot-outreach bugün
> 605k'ya çıkarken politika bir kez bile onu aday göstermedi. Doğru politika
> bağlam-öncelikli olmalı: eşik aşıldığında **ilk boş ana** compact, 24 saat
> beklemeden. Bu, model seçiminden bağımsız ve bende yapılacak iş.

**4b. Tur sayısı (teslimat batch'leme) — tavan ≤%40.** Turların kaynağı:
dış mesaj 417 tur (%40), **insan (Tuna'nın pane'e yazdıkları) 415 tur (%40)**,
sistem/komut ekosu 210 tur (%20). Batch'leme yalnız ilk gruba dokunabilir;
gerçek tavan, o mesajların kaçının birbirine yakın gelip birleştirilebileceğine
bağlı _(ada'dan varış aralığı dağılımı istendi)_.

> **Çerçeve düzeltmesi (ada, kendi önceki yorumunu çürüttü):** bu yanma
> "otonom kaçak" değil — günün %40'ı doğrudan Tuna'nın yazdıklarından doğdu.
> probot-outreach kaçak agent değil, **yoğun çalışan** agent. Ayrım sayfada
> durmalı, yoksa yanlış kaldıraç seçilir: insanın mesajları optimize edilecek
> bir israf değildir.

## Ayrı anahtar meselesi (Tuna "sonra" dedi, kapanmadı)

Kullanılan OpenRouter anahtarı **kitap projesinin** (`/srv/kitap/.env`, aylık 20
USD). İki yönlü risk: tavan dolarsa yalnız Hermes değil **kitap tarafı da
durur** (server-main tespiti). Ayrı anahtar bu bağı keser.

## Kalan belirsizlikler

- Ox Alpha'nın kapanma tarihi kesin değil ("~27 Ağu" söylenti).
- Bugünün hacmi tipik bir gün mü? Tek günlük ölçüm; filo Hermes'i yeni kullanıyor.
