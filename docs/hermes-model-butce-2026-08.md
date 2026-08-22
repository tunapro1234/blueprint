# Ox Alpha kapanırsa — model ve bütçe seçenekleri

**Ölçüm: 22 Ağu 15:20–22:46 (8 paralel Hermes agent, ~4.75 saat) + Claude tarafı
23 Ağu 00:00. İş ölçüm sırasında sürüyordu — sayılar canlı.**
Kaynaklar: `~/.hermes/state.db` (2070 çağrı), OpenRouter `/key` ve `/models`,
`/srv/server-main/olcum/2026 0822-*.md` (ada).

**Karar sorusu:** Ox Alpha (`x-preview-f-free`, anahtarsız bedava) ~27 Ağustos'ta
kapanabilir. Kapanırsa Hermes filosu neyle çalışır, günlük bedeli nedir?

---

## Önce iki düzeltme (ikisi de benim önceki ifademi çürütüyor)

**1. Yedeğe düşüren şey rate-limit DEĞİL, Ox Alpha'nın kararsızlığı.** Log'da tek
bir gerçek HTTP 429 yok. Tetikleyiciler: 93× HTTP 503, 115× timeout, 36×
bağlantı hatası. Yani bugünkü 1.05 USD **kota aşımının değil, uptime'ın bedeli**
— ve paralı pay hacimle değil, Ox Alpha'nın o günkü sağlığıyla oynuyor. Dün
"rate-limit yiyor" diye ilettiğim şey yanlıştı.

**2. Çapa testi %10.6 ıskaladı, sebebi bulundu.** Modelim 0.9478 USD dedi, gerçek
1.0486. Neden: `deepseek-v4-flash` tek fiyat değil — OpenRouter 17 upstream'e
dağıtıyor ve aralık **7.5 kat** (0.0587 → 0.44 $/M). Çapayı tutturan etkin fiyat
0.0649 $/M. Aşağıdaki tüm rakamlara bu **×1.106 routing primi** uygulandı.
(Prim deepseek'in dağılımından türedi; başka modele taşınması varsayımdır.)

## Bugünün ölçülmüş hacmi

| | çağrı | toplam prompt | bunun cache'i | çıktı |
|---|---:|---:|---:|---:|
| Ox Alpha (bedava) | 1 732 | 196.4M | %96.6 | 398k |
| OpenRouter (paralı yedek) | 338 | 39.6M | %75.5 | 234k |
| **toplam** | **2 070** | **236.0M** | **%93.1** | **633k** |

Çağrıların %98.7'si 8 outreach oturumundan; en büyük tek prompt **308k token**
(model seçiminde bağlam alt sınırı). Bu hacim **4.75 saatlik** — 8 saatlik iş
gününe ölçeklenirse aşağıdaki rakamlar ×1.68.

---

## Seçenekler

Günlük maliyetler **S2 senaryosu**: cache oranı olarak OpenRouter'da fiilen
ölçülen %75.5 alındı. (Ox Alpha'nın %96.6'sı korunursa maliyet ~%35 düşer,
cache hiç tutmazsa ~2.5 kat artar — bu tek parametre nihai rakamı 3.9 kat
oynatıyor, en kırılgan varsayım bu.)

| # | yol | günlük | 20 USD kaç günde | ne bozulur |
|---|---|---:|---:|---|
| 1 | deepseek-v4-flash birincil (bugünkü yedek) | **6.15 $** | 3.3 gün | bilinen davranış, sürpriz yok |
| 2a | upstage/solar-pro4 | **3.19 $** | 6.3 gün | 524k ctx (308k prompt sığar, marj dar); kalite ölçülmedi |
| 2b | qwen3.7-flash | **3.19 $** | 6.3 gün | 1M ctx; kalite ölçülmedi |
| 2c | gpt-5-nano | 4.46 $ | 4.5 gün | 400k ctx — marj çok dar |
| 3 | bedava + araçlı modeller (`:free`) | 0 $ | — | 3 aday 308k'yı kaldırıyor (nemotron-3.5-lightning, nemotron-3-ultra, dots-3-note-preview); **rate-limit kuralları API'den okunamadı, ölçülemedi** — Ox Alpha'nın yerine geçer ama aynı kararsızlık riski |
| 4 | Claude subagent'a dönüş | Claude kotası | — | bugün süpervizyon tek başına 36.3M birim yaktı; Hermes'siz senaryoda **işin kendisi de üstüne biner** — bu rakam alt sınırdır (ada) |
| 5 | deepseek-v4-pro | 33.84 $ | 0.6 gün | — |

**Kritik sayı:** bugünkü hacimde **hiçbir paralı seçenek 20 USD'lik aylık
anahtarla bir haftayı geçmiyor.** En ucuzu 6.3 gün. Yani soru "hangi model"den
önce "hangi bütçe" — mevcut anahtar aylık değil, haftalık bir kaynak olur.

## Birim olarak "gün" değil, "iş oturumu"

"Günlük" rakamlar aslında 4.75 saatlik tek bir salvonun bedeli — gün, ölçüm
penceresinin şekli, işin doğal birimi değil. Çağrıların %98.7'si 8 outreach
oturumundan geldi, oturum başına ortalama **256 çağrı / 2.02M taze girdi /
27.4M cache-read / 78k çıktı**. Aynı maliyetler oturum birimiyle:

| model | iş oturumu başına | 20 USD kaç oturum |
|---|---:|---:|
| qwen3.7-flash / solar-pro4 | **0.40 $** | ~50 |
| gpt-5-nano | 0.56 $ | ~36 |
| deepseek-v4-flash | **0.77 $** | ~26 |
| deepseek-v4-pro | 4.23 $ | ~5 |

**Bu birim N=1 zaafını kısmen kapatır:** gelecekteki herhangi bir iş günü,
"kaç Hermes oturumu koşacak" sorusuyla önceden fiyatlanabilir — ikinci bir
ölçüm gününü beklemeye gerek kalmadan. Kırılan varsayım yalnızca "oturumlar
birbirine benzer" olur; bugün oturumlar arası maliyet farkı **6 kat** (0.044 ↔
0.27 USD) ve sebebi işin büyüklüğü değil, o oturumun ne kadar yedeğe düştüğü —
yani Ox Alpha kapandıktan sonra bu fark daralmalı, çünkü herkes aynı lane'de
olacak.

## Süpervizyon kaldıraçları (ayrı kalem — Claude kotası, USD değil)

Bunlar model seçiminden bağımsız ve toplanmamalı:

| kaldıraç | tavan | gecikme | tekrar |
|---|---:|---|---|
| bağlam-eşikli compact | ~%11 | yok | her zincirde |
| teslimat batch'leme | %5 | var (60 sn) | yığılmada |

`bp compact` bugün **24 saat boşta + 200k** istiyor; gün boyu çalışan agent bu
filtreden hiç geçmiyor — probot-outreach 605k'ya çıktı, politika onu bir kez
bile aday göstermedi. Düzeltmesi bende hazır, **Tuna onayı bekliyor** (filo
geneline dokunur).

## Ayrı anahtar

Kullanılan anahtar **kitap projesinin** (aylık 20 USD). Tavan dolarsa yalnız
Hermes değil **kitap tarafı da durur**. Yukarıdaki "3–6 günde dolar" tablosuyla
birlikte okununca: ayrı anahtar artık "sonra"ya bırakılabilir bir konu değil.

## blueprint görüşü (karar değil)

Ox Alpha ölürse: **2b (qwen3.7-flash) birincil + deepseek-flash yedek**, ayrı
anahtarla ve aylık tavanı 20 USD'nin üstünde. Bedava `:free` adaylar cazip ama
ölçülemeyen rate-limit'leri bugün yaşadığımız kararsızlığın aynısını getirebilir
— önce küçük bir yükle denenmeli, filo ona bağlanmadan.

## Bilinmeyenler

- Cache oranının yeni sağlayıcıda korunup korunmayacağı (**3.9 kat** etki).
- `:free` modellerin gerçek kotaları — API'den okunamıyor, tahmin edilmedi.
- Ox Alpha'nın token muhasebesini doğrulayacak bağımsız çapa yok; hacmin %83'ü o.
- 8 adet `google/gemini-3.6-flash` yardımcı çağrısı hiçbir yerde kayıtlı değil
  (fiyatı deepseek'in 12–32 katı) — payı ölçülemedi.
- **N=1.** Tek gün, tek salvo, tek iş tipi (outreach). %11/%5 tavanları ve
  buradaki hacim ikinci bir yoğun gün ölçülmeden kural sayılmamalı.
- Bu turda üç hipotez veriyle düştü: "çoğu tur koordinasyondur", "otonom kaçak
  yanma" (ada) ve "batch'leme büyük kaldıraçtır" (blueprint). Sayılara güvenin
  kaynağı bu — hiçbiri beklentiyi doğrulamak için seçilmedi.
