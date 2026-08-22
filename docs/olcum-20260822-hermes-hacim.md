# Hermes filosu — ölçülmüş maliyet verisi (22 Ağustos 2026)

Ölçüm anı: 2026-08-23 00:00 +03 (= 2026-08-22 21:00 UTC).
Veri penceresi: 2026-08-22 15:20:30 → 22:46 yerel saat (log ilk/son satır).
**Karar/öneri YOK — sadece sayı ve nasıl elde edildiği.**

---

## 0. Metodoloji: veri kaynağı seçimi (önemli — brief'teki varsayımı düzeltiyor)

Brief `agent.log`'u işaret ediyordu. Log'u çözdüm, ama **daha iyi ve tam bir kaynak buldum:**
`/root/.hermes/state.db` → `session_model_usage` tablosu.

### Log formatı (çözüldü)
Token muhasebesi taşıyan **iki** satır tipi var:

```
INFO [<session_id>] agent.conversation_loop: API call #N: model=M provider=P in=A out=B total=C latency=Ds cache=E/A (F%)
INFO [<session_id>] agent.background_review: Background review complete: thread=bg-review calls=N in=G out=H cache_read=J result=R
```

Kaynak: `/usr/local/lib/hermes-agent/agent/conversation_loop.py:4181` ve
`/usr/local/lib/hermes-agent/agent/background_review.py:1000`.

Kritik semantik farkı (koddan + tek-çağrılı bir bg-review vakasıyla doğrulandı):
- `API call` satırında **`in=` = prompt_tokens = taze girdi + cache_read** (cache DAHİL), `cache=E/A` payı cache_read.
- `background_review` satırında **`in=` = canonical input_tokens = cache HARİÇ**, `cache_read=` ayrı.
- Doğrulama vakası (`20260822_181408_b7699d`, 18:30:19): bg-review `in=7746 cache_read=47360`;
  aynı saniyedeki API call satırı `in=55106 cache=47360/55106`. **7746 + 47360 = 55106 ✅**

### İki satır tipi çakışıyor mu? EVET — bg-review çağrıları HER İKİSİNDE de var
bg-review, ana session_id'yi paylaşan ayrı bir agent forku; kendi `API call #` sayacını
1'den başlatıyor. Session başına çağrı numaraları monoton DEĞİL (yeniden başlıyor) —
python ile doğruladım. Yani **log satırlarını toplarken bg-review özet satırları
sayılmamalı** (çift sayım). 1875 `in=/out=` satırı = 1836 API-call + 39 bg-review özeti.

### Neden log yerine DB
- Log: 1836 çağrı.
- DB (`session_model_usage`): **2070 çağrı**. Fark = 234 çağrı; bunlar `task='approval'`,
  `task='title_generation'` gibi yardımcı (auxiliary) çağrılar — `API call #` satırı üretmiyorlar
  ama gerçekten API'ye gidiyorlar ve DB'de kayıtlılar.
- DB ayrıca `reasoning_tokens`, `cache_write_tokens`, `billing_provider`, `task` ve Hermes'in
  kendi `estimated_cost_usd` alanını veriyor.
- DB'nin zaman aralığı (`min(first_seen)`/`max(last_seen)`): 1787401287.7 → 1787427904.4
  = bugünkü pencereyle birebir aynı. Yani DB **sadece bugünü** kapsıyor (makine bugün kuruldu).

**Bundan sonraki tüm A/B/C/D/E sayıları DB'den.** Log doğrulama/olay analizi için kullanıldı.

DB sorgusu:
```sql
select model, billing_provider, task,
       sum(api_call_count), sum(input_tokens), sum(output_tokens),
       sum(cache_read_tokens), sum(reasoning_tokens), sum(estimated_cost_usd)
from session_model_usage group by model, billing_provider, task;
```
(salt-okur açıldı: `sqlite3.connect('file:...state.db?mode=ro', uri=True)`)

---

## A. BUGÜNÜN HACMİ

`input_tokens` = **cache HARİÇ** taze girdi. `cache_read_tokens` ayrı. Toplam prompt = ikisinin toplamı.

| Lane | Çağrı | Taze girdi (cache'siz) | Cache-read girdi | Toplam prompt | Çıktı | Reasoning | Cache hit % |
|---|---:|---:|---:|---:|---:|---:|---:|
| **Ox Alpha** (`x-preview-f-free` / opencode-free) | 1 732 | 6 618 239 | 189 757 568 | 196 375 807 | 398 211 | 35 952 | 96.6 % |
| **OpenRouter** (`deepseek/deepseek-v4-flash`) | 338 | 9 705 550 | 29 912 064 | 39 617 614 | 234 410 | 71 019 | 75.5 % |
| **TOPLAM** | **2 070** | **16 323 789** | **219 669 632** | **235 993 421** | **632 621** | **106 971** | **93.1 %** |

`cache_write_tokens` bugün her satırda **0** (ne Ox Alpha ne OpenRouter cache-write ücretlendirmesi raporlamıyor).

Alt kırılım (task bazında):

| Model | billing_provider | task | Çağrı | Taze girdi | Cache-read | Çıktı |
|---|---|---|---:|---:|---:|---:|
| x-preview-f-free | opencode-free | (ana döngü) | 1 290 | 6 027 129 | 147 088 064 | 245 889 |
| x-preview-f-free | opencode-free | background_review | 244 | 543 081 | 42 612 224 | 70 258 |
| x-preview-f-free | opencode-free | approval | 153 | 43 195 | 38 720 | 63 827 |
| x-preview-f-free | (boş) | approval | 34 | 2 089 | 16 832 | 13 612 |
| x-preview-f-free | opencode-free/(boş) | title_generation | 11 | 2 745 | 1 728 | 4 625 |
| deepseek-v4-flash | openrouter | (ana döngü) | 160 | 5 996 141 | 13 406 720 | 109 978 |
| deepseek-v4-flash | openrouter | background_review | 142 | 3 692 095 | 16 505 344 | 119 576 |
| deepseek-v4-flash | (boş) | approval | 36 | 17 314 | 0 | 4 856 |

Not: `billing_provider` boş olan satırlar (70 çağrı) Hermes tarafından fiyatlandırılmıyor
(`estimated_cost_usd = 0`). Toplam hacimde payları %0.1'in altında.

**Ortalama çağrı başına prompt: 235 993 421 / 2 070 = ~114 000 token.**
Ox Alpha ve OpenRouter için ayrı ayrı 113k ve 117k — tutarlı, yani iki lane'in
muhasebesi aynı ölçekte (cache alanları karıştırılmamış).

Gözlenen **en büyük tek prompt: 308 096 token** (`grep -oE 'in=[0-9]+ out=' agent.log | sort -n | tail`).
Bu, model seçiminde bağlam alt sınırını belirliyor.

---

## B. ÇAPA DOĞRULAMASI — model çapayı tutturuyor mu?

**Çapa (doğrulandı, ölçüm anında yeniden çekildi):**
```
GET https://openrouter.ai/api/v1/key
→ usage_daily = 1.048593714, usage_weekly = 1.048593714, usage_monthly = 1.048593714
  limit = 20, limit_remaining = 18.951406286, usage (anahtar ömrü) = 6.706906746
```
daily = weekly = monthly olması iyi haber: **bu ayki tüm OpenRouter harcaması bugünün trafiği.**
Çapa temiz.

**Fiyatlar (OpenRouter `/api/v1/models`, ölçüm anı):**
`deepseek/deepseek-v4-flash`: prompt `0.00000005866` (0.05866 $/M),
completion `0.00000011732` (0.11732 $/M), **`input_cache_read` `0.000000011732` (0.011732 $/M = girdinin %20'si)**.

**Modelim (OpenRouter lane'inin TÜM hacmi):**
```
9 705 550 × 0.05866/M  = 0.569327
29 912 064 × 0.011732/M = 0.350909
234 410 × 0.11732/M     = 0.027501
                        ---------
                          0.9478 USD
```

| Hesap | USD |
|---|---:|
| Benim modelim (cache indirimli) | **0.9478** |
| Hermes'in kendi `estimated_cost_usd` toplamı | 0.9688 |
| **OpenRouter ÇAPA (gerçek)** | **1.0486** |
| Cache indirimi yok sayılsaydı | 2.3515 |

**Sonuç: model çapayı %10.6 ALTINDAN ıskalıyor** (fark +0.1008 USD).
Yön doğru, büyüklük mertebesi doğru, ama tam tutmuyor. Cache indirimi olmasaydı %148 sapardı —
yani **cache indiriminin var olduğu ve ~%20 oranında uygulandığı kesin**, sapma başka yerden.

### Sapmanın araştırılması (3 aday, biri baskın)

**1) Provider routing dağılımı — BASKIN AÇIKLAMA.**
`/api/v1/models/deepseek/deepseek-v4-flash/endpoints` çekildi. `deepseek/deepseek-v4-flash`
tek fiyat değil; OpenRouter 17 farklı upstream'e yönlendiriyor ve fiyat aralığı **7.5 kat**:

| Endpoint | prompt $/M | completion $/M | cache_read $/M |
|---|---:|---:|---:|
| StreamLake (liste fiyatı = en ucuz) | 0.05866 | 0.11732 | 0.011732 |
| GMICloud | 0.0588 | 0.1176 | 0.01176 |
| Baidu | 0.0602 | 0.1204 | 0.01204 |
| DigitalOcean | 0.0679 | 0.168 | 0.0168 |
| DeepInfra / Sail | 0.09 | 0.18 | 0.018–0.02 |
| Novita / CoreWeave / Parasail | 0.14 | 0.28 | 0.028–0.07 |
| Cloudflare (en pahalı) | 0.44 | 1.32 | 0.014 |

Model listesindeki fiyat **en ucuz endpoint'in fiyatı**. Gerçek fatura, çağrı başına hangi
upstream'e düştüğüne bağlı. Çapayı tutturmak için gereken tek-tip çarpan:
**etkin prompt fiyatı 0.06490 $/M** (liste 0.05866). Bu, StreamLake ile DigitalOcean arasında
bir karışıma tam denk geliyor — yani sapma bu tek nedenle **fazlasıyla açıklanabilir**.

**2) Kaydedilmemiş `google/gemini-3.6-flash` çağrıları — kısmi katkı, ölçemedim.**
Log'da 8 adet:
```
WARNING agent.auxiliary_client: Auxiliary client: PAID lane engaged for auxiliary task —
OpenRouter fallback model 'google/gemini-3.6-flash' is not a :free SKU and may incur real spend.
```
`session_model_usage` tablosunda **gemini satırı YOK** → bu 8 çağrının token'ları hiçbir yerde
kayıtlı değil ama OpenRouter'a gidip faturalandı. gemini-3.6-flash fiyatı 0.75 $/M girdi,
3.75 $/M çıktı — deepseek'in 12–32 katı. 8 çağrı büyük bağlamlıysa (vision/auxiliary) tek
başına birkaç cent yapabilir. **Token sayısını ölçemedim; log da DB de tutmuyor.**

**3) Boş `billing_provider`'lı 36 deepseek "approval" çağrısı — ihmal edilebilir.**
17 314 girdi + 4 856 çıktı → 0.00159 USD. Sapmanın %1.6'sı.

**Kalan başarısız/iptal çağrılar:** log'da 93 adet HTTP 503 var (hepsi opencode-free tarafında,
OpenRouter'da değil). OpenRouter'da 1 adet HTTP 402 izi. Bunlar faturayı büyütmez.

### B'nin verdiği düzeltme katsayısı
İleriki projeksiyonlarda kullanılmak üzere: **K = 1.0486 / 0.9478 = 1.1063**
(liste fiyatı üstüne %10.6 "gerçek routing" primi). Bu katsayı deepseek-v4-flash'ın
gözlenen routing karışımından türedi; başka modele taşınması **varsayım**, ölçüm değil.

---

## C. "HEPSİ PARALI" PROJEKSİYONU

Girdi: A'daki **TOPLAM** hacim (Ox Alpha + OpenRouter):
taze girdi 16 323 789, cache-read 219 669 632, toplam prompt 235 993 421, çıktı 632 621.

Cache davranışı en büyük belirsizlik olduğu için **üç senaryo** hesapladım:

- **S1 — "cache aynen korunur":** gözlenen %93.1 cache hit oranı yeni sağlayıcıda da tutar.
  (İyimser: Ox Alpha'nın %96.6'lık hit oranı onun kendi altyapısına özgü olabilir.)
- **S2 — "OpenRouter'da gözlenen cache oranı":** toplam prompt'a fallback lane'in gerçekten
  ölçülmüş **%75.5** hit oranı uygulanır (57.8M taze / 178.2M cache).
  *Bu en savunulabilir orta senaryo — çünkü tek gerçek OpenRouter ölçümümüz bu.*
- **S3 — "cache indirimi yok":** tüm prompt tam fiyattan. Üst sınır.

`*K` sütunları = B'deki 1.1063 routing primi uygulanmış hali.

| Model | ctx | in $/M | cache $/M | out $/M | S1 | S1×K | S2 | S2×K | S3 | S3×K |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| **deepseek/deepseek-v4-flash** (mevcut yedek) | 1 048 576 | 0.0587 | 0.01173 | 0.1173 | 3.61 | **3.99** | 5.56 | **6.15** | 13.92 | **15.40** |
| **deepseek/deepseek-v4-pro** | 1 048 576 | 0.4138 | 0.03448 | 0.8275 | 14.85 | **16.43** | 30.59 | **33.84** | 98.17 | **108.61** |
| upstage/solar-pro4 | 524 288 | 0.0300 | 0.00600 | 0.1200 | 1.88 | 2.08 | 2.88 | 3.19 | 7.16 | 7.92 |
| qwen/qwen3.7-flash | 1 000 000 | 0.0300 | 0.00600 | 0.1300 | 1.89 | 2.09 | 2.89 | 3.19 | 7.16 | 7.92 |
| openai/gpt-5-nano | 400 000 | 0.0500 | 0.00500 | 0.4000 | 2.17 | 2.40 | 4.03 | 4.46 | 12.05 | 13.33 |
| meta/muse-spark-1.2-contributor | 1 048 576 | 0.1000 | 0.00200 | 0.2000 | 2.20 | 2.43 | 6.26 | 6.93 | 23.73 | 26.25 |
| deepseek-v4-flash-0731 (pinlenmiş sürüm) | 1 310 720 | 0.0800 | 0.01600 | 0.1800 | 4.93 | 5.46 | 7.59 | 8.40 | 18.99 | 21.01 |
| *google/gemini-3.6-flash (referans — kaçak aux lane)* | 1 048 576 | 0.7500 | 0.07500 | 3.7500 | 31.09 | 34.40 | 59.10 | 65.38 | 179.37 | 198.44 |

Tüm rakamlar **günlük USD** (bugünkü hacim = ~4.75 saatlik 8 paralel agent, bkz. E).

### En ucuz 3 makul araç-çağıran model — SEÇİM KRİTERİM

Filtre (`/api/v1/models` üstünde, 421 model):
1. `supported_parameters` içinde **`tools`** var (araç çağırma zorunlu).
2. `context_length >= 400 000` — **gerekçe: bugün gözlenen en büyük tek prompt 308 096 token.**
   256k'lık modeller bu iş yükünü kaldırmaz.
3. `:batch` SKU'ları **elendi** — batch API asenkron (saatler), interaktif agent'ta kullanılamaz.
   (Elenmeseydi `openai/gpt-5-nano:batch` 1.08 $/gün ile listede 1. olurdu.)
4. `openrouter/auto` elendi (pricing = -1, meta-router).
5. Kalanlar S1 maliyetine göre sıralandı.

**Sonuç — en ucuz 3:**
1. **upstage/solar-pro4** — 524k ctx, S2×K = **3.19 $/gün**
2. **qwen/qwen3.7-flash** — 1M ctx, S2×K = **3.19 $/gün**
3. **openai/gpt-5-nano** — 400k ctx, S2×K = **4.46 $/gün** (çıktı fiyatı 0.40/M — pahalı çıktı;
   çıktı payımız düşük olduğu için yine de ucuz kalıyor)

4. sırada `meta/muse-spark-1.2-contributor` (6.93), 5. `deepseek-v4-flash` (6.15 — aslında
gpt-5-nano ile muse arasında). Kalite/araç-çağırma güvenilirliği ÖLÇÜLMEDİ; bunlar sadece fiyat.

### OpenRouter'da halen ÜCRETSİZ (`:free`) + araç çağıran modeller

`m['id'].endswith(':free')` ve `'tools' in supported_parameters` filtresi, 17 sonuç:

| Model | ctx | Bizim 308k prompt'u kaldırır mı? |
|---|---:|---|
| nvidia/nemotron-3.5-lightning:free | 1 000 000 | ✅ |
| nvidia/nemotron-3-ultra-550b-a55b:free | 1 000 000 | ✅ |
| dots-studio/dots-3-note-preview:free | 512 000 | ✅ |
| thinkingmachines/inkling:free | 262 144 | ❌ (308k > 262k) |
| thinkingmachines/inkling-small:free | 262 144 | ❌ |
| poolside/laguna-s-2.1:free | 262 144 | ❌ |
| poolside/laguna-xs-2.1:free | 262 144 | ❌ |
| google/gemma-4-31b-it:free | 262 144 | ❌ |
| google/gemma-4-26b-a4b-it:free | 262 144 | ❌ |
| nvidia/nemotron-3-super-120b-a12b:free | 262 144 | ❌ |
| cohere/north-mini-code:free | 256 000 | ❌ |
| z-ai/glm-5.2:free | 256 000 | ❌ |
| nvidia/nemotron-3-nano-30b-a3b:free | 256 000 | ❌ |
| nvidia/nemotron-3-nano-omni-30b-a3b-reasoning:free | 256 000 | ❌ |
| nvidia/nemotron-nano-12b-v2-vl:free | 128 000 | ❌ |
| nvidia/nemotron-nano-9b-v2:free | 128 000 | ❌ |
| liquid/lfm-2.5-2.6b:free | 65 536 | ❌ |

**Rate-limit notu — ÖLÇEMEDİM.** OpenRouter `:free` SKU'larının kota kuralları
`/api/v1/models` yanıtında yer almıyor ve anahtarın `/key` yanıtındaki `rate_limit` alanı
`{"requests":-1,...,"note":"This field is deprecated and safe to ignore."}` diyor.
Free SKU limitleri (genelde günlük istek tavanı ve hesap kredisine bağlı kademe) bu API'den
okunamıyor; **tahmin etmiyorum**. Bugün hiçbir `:free` OpenRouter modeli kullanılmadı,
dolayısıyla ampirik ölçümüm de yok.

---

## D. AYLIK TAVAN — 20 USD limit kaç günde dolar

Varsayım: bugünkü hacim her gün aynen tekrarlanır. Routing primi (×K) dahil.

| Model | S1 (cache korunur) | S2 (OR-gözlenen %75.5 cache) | S3 (cache yok) |
|---|---:|---:|---:|
| deepseek-v4-flash | 5.0 gün | **3.3 gün** | 1.3 gün |
| deepseek-v4-pro | 1.2 gün | **0.6 gün** | 0.2 gün |
| upstage/solar-pro4 | 9.6 gün | **6.3 gün** | 2.5 gün |
| qwen/qwen3.7-flash | 9.6 gün | **6.3 gün** | 2.5 gün |
| openai/gpt-5-nano | 8.3 gün | **4.5 gün** | 1.5 gün |
| meta/muse-spark-1.2 | 8.2 gün | **2.9 gün** | 0.8 gün |
| deepseek-v4-flash-0731 | 3.7 gün | **2.4 gün** | 1.0 gün |

Referans: mevcut durumda (Ox Alpha bedava, sadece fallback paralı) 1.0486 $/gün →
20 USD limiti **19.1 günde** dolar. Bugün ayın 22'si ve limitin 1.0486'sı kullanılmış
(`limit_remaining = 18.95`).

---

## E. HACİM DAĞILIMI — kim üretti

Log bunu **veriyor.** `sessions` + `session_model_usage` tablolarından, 11 ayrı oturum:

| # | session_id | başlangıç | başlık | cwd | çağrı | taze girdi | cache-read | çıktı | OR maliyeti (Hermes est.) |
|---|---|---|---|---|---:|---:|---:|---:|---:|
| 1 | 20260822_152048_f7e457 | 15:20 | "Say ve /srv dizinini listele" | scratchpad | 8 | 93 417 | 68 032 | 523 | 0 |
| 2 | 20260822_180149_9706f1 | 18:01 | kaiser-jr.md | /srv/probot/outreach | 244 | 1 584 202 | 31 108 544 | 61 911 | 0.0701 |
| 3 | 20260822_180321_4f9c18 | 18:03 | error.md | /srv/probot/outreach | 333 | 3 482 757 | 24 888 640 | 124 638 | 0.2702 |
| 4 | 20260822_180438_76bb61 | 18:04 | joyful.md | /srv/probot/outreach | 247 | 1 790 677 | 27 939 520 | 78 585 | 0.0438 |
| 5 | 20260822_180518_a488c0 | 18:05 | kuanta.md | /srv/probot/outreach | 219 | 2 045 351 | 24 067 008 | 71 552 | 0.0880 |
| 6 | 20260822_180558_22e7e4 | 18:05 | robistim.md | /srv/probot/outreach | 101 | 857 012 | 7 484 864 | 37 214 | 0.0525 |
| 7 | 20260822_181328_50d230 | 18:13 | tel1.md | /srv/probot/outreach | 274 | 2 496 281 | 29 402 944 | 99 110 | 0.2068 |
| 8 | 20260822_181408_b7699d | 18:14 | tel2.md | /srv/probot/outreach | 315 | 2 602 835 | 43 675 264 | 82 585 | 0.1433 |
| 9 | 20260822_181448_3cfb53 | 18:14 | tel3.md | /srv/probot/outreach | 311 | 1 266 023 | 30 369 664 | 70 898 | 0.0940 |
| 10 | 20260822_205601_72808c | 20:56 | FTC eğitim araştırması | /srv/probot/egitim | 14 | 71 641 | 616 512 | 4 977 | 0 |
| 11 | 20260822_210434_73668b | 21:04 | pane teslim testi | scratchpad | 4 | 33 593 | 48 640 | 628 | 0 |

(Tablo `select session_id, sum(...) from session_model_usage group by session_id` ile üretildi;
`sessions.api_call_count` alanı bg-review alt-çağrılarını saymadığı için kullanılmadı.)

**Yorumlanabilir sayılar:**
- **8 "gerçek" iş oturumu**, hepsi `/srv/probot/outreach` (2–9 arası). Hepsi 18:01–18:14
  arasında, ~13 dakika içinde başlatılmış → **eşzamanlı bir filo salvosu.**
- Bu 8 oturum toplam çağrının **%98.7'sini** üretti (2 044 / 2 070).
- **İş oturumu başına ortalama: 256 çağrı, 2.02M taze girdi, 27.4M cache-read, 78k çıktı.**
- Süre: 18:01 başlangıç → 22:46 son log satırı = **~4.75 saat**. Yani "günlük" hacim
  aslında ~4.75 saatlik 8-paralel-agent yükü. **8 saatlik bir iş gününe ölçeklenirse
  C/D tablolarındaki tüm rakamlar ×1.68 alınmalı.** (Bu ekstrapolasyonu tablolara
  uygulamadım — ölçülmüş değil.)
- Oturum başına maliyet dağılımı çok eşitsiz: en pahalı (error.md, 0.27 $) ile en ucuz iş
  oturumu (joyful.md, 0.044 $) arasında **6 kat** fark var. Sebep: fallback'e ne kadar düştüğü.

---

## F. BELİRSİZLİKLER — modelin en kırılgan yerleri

Kırılganlık sırasına göre:

**1) Cache-read oranının yeni sağlayıcıda korunacağı varsayımı — EN KIRILGAN.**
Toplam prompt'un **%93.1'i cache-read**. S1↔S3 arasındaki fark **3.9× maliyet**
(deepseek-flash'ta 3.99 vs 15.40 $/gün). Yani nihai rakam neredeyse tamamen bu tek
parametreye bağlı. Ox Alpha %96.6 hit yapıyor; aynı iş yükü OpenRouter'a düştüğünde
gözlenen hit **%75.5**'e iniyor. Neden düştüğünü ölçemedim (fallback çağrıları soğuk
başlangıç olduğu için mi, yoksa deepseek'in cache TTL'i daha kısa olduğu için mi —
ayırt edecek verim yok). **Tuna'ya sunulacak sayı S2 olmalı, S1 değil** — S2 tek gerçek
OpenRouter ölçümüne dayanıyor.

**2) Routing primi K=1.1063'ün başka modellere taşınması.**
K, deepseek-v4-flash'ın 17-endpoint'lik dağılımından türedi. gpt-5-nano tek sağlayıcılı
(OpenAI) → K muhtemelen 1.00. qwen/solar için bilinmiyor. **K'yı tüm satırlara uyguladım,
bu bir varsayım.** K'sız sayılar da tabloda (S1/S2/S3 sütunları).

**3) Log/DB tüm çağrıları kapsamıyor — ölçülmüş boşluk var.**
- Log 1836, DB 2070 çağrı → **log %11 eksik** (auxiliary çağrılar).
- DB bile eksik: **8 adet `google/gemini-3.6-flash` çağrısı** log'da uyarı olarak görünüyor
  ama `session_model_usage`'da hiç satırı yok. Token'ları hiçbir yerde kayıtlı değil.
  Bunlar B'deki %10.6 sapmanın bir kısmını açıklıyor olabilir; **payını ölçemedim.**
- Yani gerçek çağrı sayısı ≥ 2078, kesin sayı bilinmiyor.

**4) Fallback'in tetikleyicisi brief'te yazılandan FARKLI — projeksiyonu etkiler.**
Brief "rate-limit olunca yedeğe düşüyor" diyor. Log'da **tek bir gerçek HTTP 429 yok**
(`grep '429'` eşleşmelerinin hepsi tarih/token içindeki rastgele rakamlar). Gerçek tetikleyiciler:
- 93 adet **HTTP 503** (opencode-free upstream'i düşüyor),
- 115 adet `transient transport error ... Request timed out`,
- 36 adet `Auxiliary approval: connection error on auto ... trying fallback`,
- 19 adet `Fallback activated: x-preview-f-free → deepseek-v4-flash (openrouter)`.

Yani bugünkü 1.0486 $ **kota aşımının değil, Ox Alpha'nın kararsızlığının** bedeli.
Sonuç: **fallback oranı Ox Alpha'nın uptime'ına bağlı, hacme değil** — bugünkü %16.8'lik
(39.6M / 236M prompt) paralı pay, Ox Alpha'nın bugünkü kararlılığının fonksiyonu ve
gün gün büyük oynayabilir.

**5) Bugünün tipik bir gün olduğu varsayımı.**
Tek gün, tek salvo (8 agent, 4.75 saat), tek iş tipi (`/srv/probot/outreach` outreach görevleri).
Başka bir iş karışımı (daha uzun oturumlar, daha çok araç çıktısı, vision) profili tamamen
değiştirir. **N=1.** Log rotasyonu yok, `state.db` 15:18'de oluşmuş — geçmiş gün verisi
sunucuda mevcut değil, karşılaştırma yapamıyorum.

**6) Ox Alpha'nın gerçek token muhasebesinin doğruluğu.**
`x-preview-f-free` satırlarında `estimated_cost_usd = 0`, `cost_status = 'unknown'`,
`cost_source = 'none'` — yani hiç fiyat kaydı yok, sadece token sayacı var. Bu sayaçların
opencode-free'nin raporladığı `usage` bloğuna dayandığını varsayıyorum. Doğrulayacak
bağımsız bir çapa YOK (Ox Alpha'nın bir kullanım API'si yok/bakmadım). Ox Alpha'nın
token raporlaması yanlışsa C ve D'deki **tüm** "hepsi paralı" projeksiyonu kayar —
ve bu hacmin %83'ü.

---

## Ek: kullanılan komutlar

```bash
# log formatı
grep -E 'in=[0-9]+ out=[0-9]+' agent.log | sed -E 's/[0-9]+/N/g' | sort | uniq -c | sort -rn
# emitter kaynağı
sed -n '4170,4190p' /usr/local/lib/hermes-agent/agent/conversation_loop.py
sed -n '1000,1012p' /usr/local/lib/hermes-agent/agent/background_review.py
# 429 gerçekten var mı
grep '429' agent.log | sed -E 's/[0-9]+/N/g' | sort | uniq -c
grep -oE 'HTTP [0-9]+' errors.log agent.log | sort | uniq -c
# fiyatlar
curl -s https://openrouter.ai/api/v1/models
curl -s https://openrouter.ai/api/v1/models/deepseek/deepseek-v4-flash/endpoints
# çapa
curl -s -H "Authorization: Bearer $KEY" https://openrouter.ai/api/v1/key
# hacim (salt-okur)
sqlite3.connect('file:/root/.hermes/state.db?mode=ro', uri=True)
```

Yazma/değiştirme yapılmadı: tmux'a dokunulmadı, config/.env okundu ama değiştirilmedi,
state.db salt-okur URI ile açıldı, commit atılmadı.
