# blueprint yol haritası

Karar tarihi: 2026-07-27. Bu dosya **kararlaştırılmış ama henüz yazılmamış** işleri tutar.
Yapılmış işler burada değil, git geçmişinde.

## Yön (Tuna, 2026-07-27)

**bp minimal kalacak ve açık kaynak olacak.** Claude Code'un çalışma biçimine karışan,
kullanıcıyı araca göre şekillenmeye zorlayan özellikler istenmiyor — araç kullanıcıya uyar.
Rakip taramasında (34 araç: HN + Product Hunt + GitHub) görülen "her şeyi yapan kokpit"
yaklaşımı bilinçli olarak reddedildi.

Ayırt edici olduğumuz üç yer — geliştirmeye değer olanlar bunlar:
1. **Agent'lar arası iletişim / federation** (en umut vadeden)
2. **Ölçüm**: token muhasebesi, cache/context durumu
3. **Filo işletimi**: kalıcı hiyerarşi, kuyruk, bildirim

Taramada 34 aracın **hiçbirinde** otomatik compaction ve gerçek context/cache görünümü yoktu;
token takibi yalnızca üçünde vardı, hiçbirinde prompt bazlı kırılım yoktu.

---

## 1. Eski konuşmalarda arama

**Ne:** `bp search "<sorgu>" [--agent <ad>] [--since 7d]` — oturum jsonl'lerinde tam metin arama;
sonuçta agent, tarih, prompt önizlemesi ve session id.

**Neden:** ccmux/HN yorumlarında en çok istenen ama hiçbir araçta olmayan şey: *"günler sonra
bağlamı yeniden kurmak, eski oturumu içerikten aramak."* Bizde veri zaten var — `internal/tokens`
prompt geçmişini (90 gün) tutuyor, `bp tokens --prompts` onu okuyor.

**Not:** Gas Town'daki "Seance" fikri (kapanmış bir oturuma tam transcript'i yüklemeden tek soru
sorma) bunun üzerine kurulabilir; önce düz arama.

## 2. Bildirim kanallarını çoğaltma

**Ne:** WhatsApp'a ek olarak en az bir push kanalı (**ntfy** ilk aday: self-hosted, hesap
gerektirmiyor, ~20 satır). Kanal seçimi config'ten; mevcut `bp wa` davranışı değişmez.

**Neden:** Agent Deck'te Telegram/Slack/Discord/ntfy var; bizde tek kanal ve o da kişisel
WhatsApp'a bağlı. Bir kanal düşerse bildirim tamamen kesiliyor.

## 3. Federation'ı derinleştirme

Şu an çalışan: hub (`bp-tunnel.tunapro.xyz`) + Bearer token + expose allowlist + poll/ack.
Araştırma (A2A 1.0, ANP, AGNTCY, OWASP AI Agent Security Cheat Sheet) sonrası kararlaştırılanlar:

**3a. Mesaj zarfı (minimal):** `id`, `from`, `to`, `type`, `thread_id`, `created_at`, `expires_at`,
`idempotency_key`, `body`. `from` alanını **istemci değil hub** üretir — token'dan çıkarılır.
(Bizde zaten böyle; zarfı standartlaştırmak A2A ile ileride uyumu kolaylaştırır.)

**3b. Teslimat semantiği:** exactly-once **hedeflenmeyecek**. at-least-once + benzersiz id +
idempotent alıcı + açık ACK + cursor'lı polling. (Poll/ack şu an var; cursor eksik.)

**3c. Güvenlik — en kritik açık madde:** dış mesaj şu an doğrudan tmux pane'ine yazılıyor.
OWASP'ın birinci maddesi bunun tersini söylüyor: **dış mesaj komut gibi enjekte edilmemeli**,
spool/inbox'a yazılmalı ve agent onu açıkça okumalı. Ayrıca mesaj `trust: external-untrusted`
etiketiyle, sistem talimatlarından ayrı bir veri alanında sunulmalı.
→ Karar: en azından **görünür kaynak etiketi + agent kuralı** (dış mesaj = talep, emir değil).
Tam inbox modeli minimalizmi bozarsa ikinci aşamaya bırakılır.

**3d. A2A uyumu:** şimdilik **hayır**. A2A 1.0 (Mart 2026, Linux Foundation) olgun ama bizim
iki-kişilik senaryo için ağır. Zarf alan adlarını A2A'ya yakın tutmak yeterli — ileride köprü
yazmak kolaylaşır.

**3e. P2P değil hub:** karşı taraf NAT arkasında; hub tek sabit uç, her iki filo dışarı bağlanır.
Metateam'in P2P yaklaşımı daha zarif ama işletmesi zor.

## 4. Devam eden (yazılmış, deploy bekliyor / yarım)

- **Teslim anında compact** — soğuk + >200k agent'a mesaj gitmeden önce `/compact`;
  daemon kuyruğunu bloklamayan durum makinesi. (codex turu tamamlandı, review bekliyor)
- **Context eşiği uyarısı** — >300k'da `!` işareti ve log; "sıcak ama devasa" vakası için.
- **Opus review + sadeleştirme turu** — gpt'nin yazdığı federation/pending/cache kodunun
  MVP ölçüsüne çekilmesi. Tuna'nın açık isteği.

## 5. Kuyruktaki hata: `bp open` yeni kayıtta parent/class çıkarımı

ada 2026-07-27'de ikinci kez elle düzeltmek zorunda kaldı. Aciliyet düşük, sıradaki işi bölmüyor.

**Belirti:** `bp open kavram-launch /srv/kavram/launch` →
`{"class":"other","parent":"server-main"}`. Beklenen `{"class":"kavram","parent":"kavram-main"}`,
çünkü book'ta `kavram-main` folder=`/srv/kavram` ve aynı kalıptaki `kavram-gate`/`kavram-yc`
zaten `kavram-main` altında.

**Sebep:** `bp open` parent olarak çağıran agent'ı geçiriyor (`cmd/bp/main.go` → `a.sender()`),
`book.SetStatus` ise yeni kayıtta `class`'ı sabit `"other"` yazıyor. Yol bilgisi hiç kullanılmıyor.

**Karar (ada'nın önerisi, özel-durum kodu yok):** yeni kayıtta folder yolunu mevcut agent'ların
folder'larıyla karşılaştır, **en uzun eşleşen üst-yol** kimse `parent` o olsun, `class` da onun
class'ı. Eşleşme yoksa bugünkü davranış (`server-main` / `other`) kalsın. Böylece
`/srv/probot/...` → `probot-main`, `/srv/kitap/...` → `kitap-main` kendiliğinden bağlanır.

Uygulama notları:
- Karşılaştırma **yol sınırında** olmalı: `/srv/kavram` `/srv/kavram-old`'u eşleştirmemeli.
- Book folder'ı açıklama taşıyabiliyor (`"/srv (home: /srv/server-main)"`) — `cmd/bp/bar.go`
  içindeki `firstPath` bunu zaten temizliyor, ortak bir yere taşınmalı.
- `server-main` folder=`/srv` her şeyi eşleştirir; en-uzun kuralı doğal olarak derini seçer,
  yani fallback kendiliğinden doğru çalışır.
- **Doğru book'a yazılmalı:** eşleşme hangi book'ta bulunduysa kayıt da oraya gitsin
  (`/srv/probot` altı ProbotPath, diğerleri MainPath). `SetStatus` şu an ad eşleşmesi yoksa
  `paths[0]`'a yazıyor.
- Sadeleştirme fırsatı: `book.AddLive` aynı çıkarımı **ada göre** (`kavram-` öneki) yapıyor.
  İkisi tek fonksiyonda birleşmeli — önce yol, sonra ad; iki ayrı kural bakımı zorlaştırıyor.

**Ayrı ve KAPALI olan madde:** ada'nın daha önce bildirdiği "`bp open` mevcut kayıtların
parent/role alanlarını eziyor" hatası `909f20f` ile düzeldi (`SetStatus` artık var olan kayıtta
yalnızca `status` yazıyor, değişiklik yoksa dosyaya hiç dokunmuyor). Asıl sebep `bp open` değil,
daemon keepalive'inin 30 saniyede bir tüm dosyayı yeniden yazmasıydı.

## 6. Codex app-server desteği (Yiğit'in filosuna bağlanmak)

kavram-launch'ın 2026-07-27 görevi. Yiğit'in agentları tmux'ta değil: tek uzun ömürlü
**`codex app-server`** daemon'u altında "thread" olarak yaşıyorlar. Federation mesajı taşır ama
onların filosunu **göremiyoruz**. Bu bölüm o boşluğu kapatıyor.

### 6a. Mimari — ne olduğu (yerelde doğrulandı, codex-cli 0.145.0)

`codex app-server`, **JSON-RPC 2.0** konuşan uzun ömürlü bir daemon. Çerçeveleme satır-sonlu
düz JSON (Content-Length yok). Taşıma: `--listen stdio:// | unix://PATH | ws://IP:PORT`.
Yerelde `--stdio` ile el sıkışıp `initialize` + `thread/list` + `model/list` +
`account/rateLimits/read` çağrılarını gerçekten çalıştırdım; protokol **89 istemci metodu**,
**70 sunucu bildirimi** içeriyor.

Kardeş komutlar ve ne oldukları:
- **`app-server daemon start|stop|restart|version`** — yönetilen (managed) daemon.
  `~/.codex/app-server-control/app-server-control.sock` kontrol soketini o yaratır.
  ⚠️ **standalone kurulum şart** (`~/.codex/packages/standalone/current/codex`); bizim npm/node
  kurulumumuzda çalışmıyor ("managed standalone Codex install not found"). Yiğit'te var.
- **`app-server proxy --sock <path>`** — stdio baytlarını çalışan daemon'un kontrol soketine
  köprüler. **Uzaktan bağlanmanın SSH-dostu kapısı budur.**
- **`codex --remote ws://|wss://|unix://PATH`** — TUI'yi uzak bir app-server'a bağlar.
- **`remote-control start|stop|pair`** — aynı daemon + uzaktan kontrol; `pair` kısa ömürlü
  eşleştirme kodu basar. Yiğit telefondan böyle bağlanıyor. Araya OpenAI tarafında bir relay
  girdiği anlaşılıyor (doğrulanmadı).
- **`mcp-server`** — Codex'i MCP sunucusu yapar (stdio). Bu **başka bir yüzey**: bir LLM'in
  Codex'i araç olarak çağırması içindir, filo yönetimi için değil. Karıştırılmamalı.
- **`exec-server`** — ws:// dinleyen bağımsız exec servisi; `--remote` ile kendini "remote
  environment" olarak kaydedebiliyor. Codex Cloud tarafına ait, bizim senaryomuzla ilgisiz.

### 6b. Neden bizi ilgilendiriyor — protokol bp'nin yaptığı her şeyi zaten içeriyor

| bp bugün (tmux'ta) | app-server'da yerel karşılığı |
|---|---|
| `bp tree` / `status` — kim var | `thread/list` (`cwd`, `searchTerm`, `cursor`, `limit`) |
| meşgul mü? — `capture-pane` çıktısı okunarak | `thread/status/changed`, `turn/started`, `turn/completed` (**push**) |
| `bp msg` (boştaki agent'a) | `turn/start` |
| çalışan tura mesaj — **bizde yasak** | `turn/steer` — birinci sınıf, işi bölmeden |
| işi kesme | `turn/interrupt` |
| teslim anında compact (§4) | `thread/compact/start` |
| context boyutu — jsonl kuyruğu okunarak | `thread/tokenUsage/updated` (**push**) |
| `bp usage` / `bp tokens` | `account/rateLimits/read`, `account/usage/read` |
| agent adı (customTitle kaydı) | `thread/name/set` |
| — (bizde yok) | `thread/goal/set|get|clear` — otonom hedef motoru |

**Dürüst sonuç:** Codex tarafında bp'nin tmux pane'i kazıyarak tahmin ettiği her şeyi daemon
zaten *kesin* veriyor. Yani bp'nin oradaki değeri daha iyi bir taşıma katmanı olmak değil;
**iki filonun üstündeki ortak katman** olmak: isim/hiyerarşi, kuyruk, bildirim, federation.
Bu ayrım roadmap'in geri kalanının çerçevesi — bp bir Codex arayüzüne dönüşmemeli.

**Not:** `thread/list` içindeki `searchTerm` ile "blueprint" araması boş döndü; büyük olasılıkla
yalnızca thread adında arıyor. Yani §1'deki içerik araması hâlâ bize ait bir iş.

### 6c. Bağlanma yolu — karar

**Seçilen: SSH üzerinden `app-server proxy`.** Uzak makinede `codex app-server proxy --sock
~/.codex/app-server-control/app-server-control.sock` çalıştırılır, bp onun stdin/stdout'una
satır-sonlu JSON-RPC konuşur. Yeni port yok, relay yok, bizim tarafta daemon yok, Yiğit'in
sisteminde kurulum yok.

Elenenler: **`ws://`** (Tailscale/port açmayı gerektirir), **`remote-control` eşleştirme**
(filo içi trafiğe üçüncü taraf relay sokar), **`codex exec resume`** (bugünkü yöntemimiz —
thread'e zorla mesaj enjekte ediyor ve çalışan otonom goal'u bölebiliyor; `turn/steer` bunun
doğrusu).

### 6d. Yapılacak işler (sırayla, MVP ölçüsünde)

1. **`internal/codexrpc`** — küçük JSON-RPC istemcisi: bir alt süreç aç (yerelde
   `codex app-server --stdio`, uzakta `ssh … app-server proxy`), `initialize` yap, istek/cevap
   eşle, bildirimleri bir kanala akıt. Stdlib yeter, ~200 satır.
2. **Salt-okur gözlem** — `bp status` / `bp tree` içinde Codex thread'lerini ikinci bir arka uç
   olarak göster (ad, cwd, meşgul mü, context). **Yazma yok.** İlk teslim burada bitsin.
3. **Mesaj teslimi** — `bp msg <thread>@codex`: tur çalışmıyorsa `turn/start`, çalışıyorsa
   `turn/steer`. Federation adreslemesiyle (`ad@peer`) aynı sözdizimi.
4. **(Belki)** context/limit ölçümünü `account/rateLimits/read` ve `thread/tokenUsage/updated`
   ile besleyip `bp usage`'a Codex sütunu eklemek.

**Kapsam dışı (şimdilik):** thread başlatma/silme, goal yönetimi, onay akışları (`turn/steer`
dışındaki her yazma). Bunlar bp'yi Codex kokpitine çevirir — §"Reddedilenler"deki gerekçeyle
tutarsız olur.

### 6e. Mimari risk ve tek açık doğrulama

**Risk:** bp bugün "agent = tmux oturumu" varsayımı üstüne kurulu. İkinci bir arka uç eklemek,
bilinçli reddettiğimiz "her şeyi yapan kokpit" yönüne kayma riski taşır. Koruma: ince bir
**transport arayüzü**, tmux varsayılan ve tek yazma yolu olarak kalır, Codex arka ucu önce
salt-okur girer. Arayüz iki arka ucu da temiz tutamıyorsa iş durur, zorlanmaz.

**Açık doğrulama (kavram-gate'ten istendi 2026-07-27):** `app-server proxy`, Yiğit'in
*çalışan* daemon'una bağlanıp `thread/list` döndürüyor mu? Yerelde yönetilen daemon'u
kuramadığımız için (standalone kurulum yok) bu yalnızca uzakta sınanabilir. **Cevap olumsuzsa
6c'deki taşıma kararı yeniden açılır** — o durumda geriye kalan makul seçenek Tailscale
üzerinden `ws://`.

---

## Reddedilenler (tekrar tartışılmasın diye)

- **Zamana dayalı otomatik compaction** — bedeli peşin, boşta agent zaten maliyetsiz.
  Yerine teslim anında compact.
- **cmux tarzı ayrı terminal emülatörü** — fazla bloat; tmux yeterli.
- **HN postu (şimdilik)** — "bir tmux orkestratörü daha" pazarı dolu ve HN yorumları acımasız
  ("overengineering", "yeni TODO app"). Post açılacaksa açı ölçüm verisi olmalı, araç değil.
- **`compact_before_task` agentbook bayrağı** — MVP'ye alınmadı.
- **`--warm-window` / `--parent` gibi ek bayraklar** — sabit varsayılanlar yeterli.
