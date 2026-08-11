# `bp doctor` — canlılık kontrolü tasarımı

Durum: **plan**. İstek: server-main/ada, 2026-08-10 (probot-business bulgusu üzerine).

## Neden ayrı bir komut, neden metrik yetmez

Olay: 7 Ağustos'ta codex OAuth oturumu iptal edildi. Collector 401 alıp `codex_5h: null` yazdı;
`bp usage` bunu "kimse koşmadı" gibi gösterdi. Gerçek "kimse KOŞAMIYOR"du — filo çapında arıza.
5–10 Ağustos arasındaki gönüllü freeze arızayı **tam olarak maskeledi**: sistem zaten durduğu için
sıfır trafik normal görünüyordu.

Buradan çıkan genel ilke şu ve tek bir alan eklemekle çözülmez:

> **Pasif metrik sessizliği arızadan ayırt edemez.** Durdurulmuş bir sistemde "0" ile "kırık" aynı
> görünür. Ayrımı yapabilen tek şey, sorulduğu anda **aktif** kontrol eden bir yoklamadır.

Bu yüzden iki iş var ve ikisi de gerekli:

1. **Gösterim dürüstlüğü (hata, feature değil):** bp hiçbir yerde yokluğu değermiş gibi
   basmayacak. `Codex: %<nil>` yerine "veri yok" / "ERİŞİM YOK" ayrımı. Bu ayrı ele alınıyor
   (`internal/codexauth` + usage renderer).
2. **`bp doctor`:** talep üzerine çalışan, her bağımlılığı **yoklayan** ve "yol açık mı"
   sorusuna gerekçeli cevap veren komut. Load-bearing olan bu.

## Ölçülen tuzak — yerel süre kontrolü yanıltıyor

Kırık dosyanın gerçek hâli (10 Ağustos'ta ölçüldü):

| sinyal | değer | ne der |
|---|---|---|
| `tokens.access_token.exp` | 2026-08-17 (gelecekte) | **"sağlıklı"** — YANLIŞ |
| `tokens.id_token.exp` | 2026-08-07 13:15 (75 saat önce) | süresi dolmuş |
| `last_refresh` | 2026-08-07 09:15 (3 gün önce) | yenileme durmuş |

Yani en bariz alana (`access_token.exp`) bakan bir kontrol, tam kesinti sırasında "yol açık"
raporlar. Doctor kararını `last_refresh` bayatlığı ve `id_token.exp` üzerine kurar;
`access_token.exp` **kanıt sayılmaz**. Bu, "şemaya bakıp tasarlarsan yanılırsın" sınıfının
ders örneği.

## Sıra kararı (2026-08-11)

probot-business'ın çerçevesi doğru ama bir düzeltmeyle: *"alan eklemek geçmişi açıklar, canlılık
kontrolü geleceği korur."* Alan **geçmişi açıklamıyor** — 7-10 Ağustos'ta yazılmış null satırlar
sonsuza dek belirsiz kalıyor, çünkü alan yalnız kendisinden SONRA yazılan satırları açıklar. İkisi
de ileriye dönük; farkları başka: alan **kanıt biriktirir**, doctor **o anda cevap verir**.

Yazılmış null'ları geriye dönük doldurmayacağız: çıkarımı ölçüm deposuna yazmak, bu hafta
temizlediğimiz "yokluğu değermiş gibi göstermek" hatasının kendisi olur. Boşluk boşluk olarak
kalır; `bp usage` zaten tarihini söylüyor ("son deger 2d 2h once").

**Sıra: (1) collector alanı, (2) doctor.** Gerekçe teknik, öncelik değil: collector'ın 401'i,
**yerel dosyadan görülemeyen** sunucu-tarafı iptali yakalayan tek kanıt. Bizim yerel okuyucumuz
(`internal/codexauth`) token'ı sunucuda iptal edilmiş ama dosyası tazeyken "OK" der. Doctor bu
kanıtı okuyabilirse olağan durumda `--probe`'a hiç ihtiyaç duymaz. Yani alan doctor'ı
güçlendiriyor; tersi değil.

### Collector sözleşmesi (usage-pulse, server-main'in dosyası — ada uygular)

Mevcut anahtarlara DOKUNMADAN, satır başına eklenir:

| alan | değer | kural |
|---|---|---|
| `codex_status` | `ok` \| `auth` \| `http` \| `network` \| `skipped` | bu satırda neden sayı yok |
| `codex_error_ts` | RFC3339 | son başarısız denemenin zamanı |
| `codex_http` | tamsayı (401, 429, 5xx) | HTTP yanıtı varsa |

- Sayılar VARSA `codex_status: "ok"` ve hata alanları **hiç yazılmaz** (null da değil) — okuyan
  taraf "yok" ile "null"u ayırt etmek zorunda kalmasın.
- Token'ın hiçbir parçası ve yanıt gövdesi yazılmaz.
- `codex_5h` haftalık figürü taşımaya devam eder (13 Tem'deki plan değişikliğinden kalan yanlış
  etiket; anahtar collector'ın sözleşmesi olduğu için yeniden adlandırılmıyor).

### Doctor'ın codex kararı bu sözleşmeyle

1. son N dakikada `codex_status: "auth"` → **KAPALI**, sunucu-tarafı kanıt, yoklama gerekmez
2. yerel `codexauth` Expired → **KAPALI**
3. yerel Stale + collector'dan yakın başarı yok → **BİLİNMİYOR**, kanıtlar listelenir
4. aksi hâlde **AÇIK** — "model çağrısı denenmedi" itirafıyla

## Kontroller (v1)

Her satır: ad · verdict (açık/kapalı/bilinmiyor) · kanıt · ne yapmalı. Tek ekranı geçmeyecek.

| kontrol | nasıl | neden burada |
|---|---|---|
| codex auth | `internal/codexauth`, yerel ve ağsız | bu olayın kendisi |
| claude auth | pane'de `AuthExpired` (4 Ağu'da eklendi) + varsa credentials süresi | 4 Ağu'da bir mesaj bu yüzden kayboldu |
| tmux | sunucu erişilebilir mi, oturum sayısı | her şeyin taşıyıcısı |
| daemon | `blueprint.service` active mi | periyodik işler + WA köprüsü |
| agentbook | okunuyor/parse ediyor mu, **kitaplar arası yinelenen ad** | 5 Ağu: 26 agent iki kitapta, 4'ü ayrışmış |
| federation | `bp fed ping` (zaten var) | dış hat sessizce ölüyor |
| kuyruk | pending derinliği + en eski öğenin yaşı | tıkanan kuyruk teslimatı durdurur |
| disk | transcript büyümesi için pay | haftalık 0,5 GB artıyor |

Sonradan: `sqlite3` varlığı (transcript indeksi gelince), codex RPC soketi (§6d).

**Dürüstlük kuralı:** doctor yapamadığı kontrolü de yazar — örn. "codex model çağrısı
DENENMEDİ (kota harcamamak için); auth yalnız yerel dosyadan okundu". Yoklamadığını yoklamış
gibi göstermek, çözmeye çalıştığımız hatanın aynısı olur.

## Ağ yoklaması: evet, ama ölçülü

Yerel dosya kontrolü bu olayı yakalar (yukarıdaki tablo), ama sunucu tarafında iptal edilmiş bir
token yerel olarak **hiç** fark edilmeyebilir. Kesin cevap için tek bir ucuz istek yeter:
collector'ın zaten çektiği kota endpoint'i (`/backend-api/wham/usage`) — **model çağrısı değil,
kota harcamaz**, ve 401 kesin kanıttır. Karar: `bp doctor` yerel kontrolleri her zaman yapar,
ağ yoklamasını `--probe` ile yapar (varsayılan kapalı, çünkü doctor'ın kendisi sessiz ve bedava
kalmalı).

## Kim çalıştırır

- **Elle:** filo ayağa kalkarken ve freeze sonrası — asıl kullanım bu. Freeze vakası tam olarak
  "durdurulmuş sistem kalkarken yol açık mı" sorusudur.
- **Daemon (ikinci aşama):** periyodik, ama **yalnız durum GEÇİŞİNDE** haber verir (açık→kapalı),
  sürekli durumda susar. Haber kanalı WhatsApp/ntfy (mevcut health-watch yolu), agent uyandırmaz —
  bekleyen duyuru asla tur uyandırmaz kuralı geçerli.

## Reddedilenler

- **Collector'a tek alan eklemek yeterli sayılmadı:** yalnız codex kotasını ve yalnız bakan
  birini kurtarır; freeze maskelemesini çözmez. Yine de gösterim ayrımı yapılacak (madde 1).
- **Sürekli aktif sağlık taraması (heartbeat servisi):** bp minimal kalacak; doctor talep üzerine
  çalışır, daemon'a yalnız geçiş bildirimi düşer.
- **`access_token.exp`'e güvenmek:** ölçüldü, kesinti sırasında "sağlıklı" diyor.
