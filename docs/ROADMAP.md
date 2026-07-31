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

## 3. Federation'ı derinleştirme

**BEKLEMEDE (Tuna, 2026-07-29):** dış hat işleri (3a-3e, güvenlik dahil) birlikte planlanana
kadar ertelendi; kendiliğinden başlanmayacak.

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

## 4. Devam eden / yarım

- **Teslim anında compact** — soğuk + >200k agent'a mesaj gitmeden önce `/compact`;
  daemon kuyruğunu bloklamayan durum makinesi. (Düzeltme 2026-07-29: codex turu 0 bayt
  çıktıyla ölmüştü — bu iş HİÇ yazılmadı, spec scratchpad'de; sıfırdan yapılacak.)
- **Context eşiği uyarısı** — >300k'da `!` işareti ve log; "sıcak ama devasa" vakası için.
- **Opus review + sadeleştirme turu** — gpt'nin yazdığı federation/pending/cache kodunun
  MVP ölçüsüne çekilmesi. Tuna'nın açık isteği.

## 5. `bp open --resume` — kalan küçük durum

Hiç başlık almamış oturum çözülemiyor, `--resume` boş dönüp yeni oturum açıyor — yanlış oturum
değil, oturumsuzluk (2026-07-28'de `probot-shop-worker`/`worker2` bu durumdaydı). Karar verilmiş
bir iş yok; acırsa ele alınır. (Parent/class çıkarımı, yinelenen kayıt temizliği ve --resume
yanlış-oturum hatası 2026-07-29'da kapandı — git geçmişi.)

## 6. Codex app-server desteği

**Önem yükseldi (Tuna, 2026-07-29): bp'yi codex kullanan insanlar da kullanabilmeli** — yalnızca
Yiğit'in filosunu görmek değil, genel bir özellik. Yiğit'in agentları tmux'ta değil: tek uzun
ömürlü **`codex app-server`** daemon'u altında "thread" olarak yaşıyorlar.

**Durum 2026-07-29: 6d/1-2 YAZILDI ve deploy edildi** (`internal/codexrpc` + config `codex.sockets`
+ `bp status`/`tree` salt-okur Codex bölümü). Gerçek tel iki sürpriz verdi, testlere gömüldü:
app-server yanıtlarında `jsonrpc` alanı hiç yok; sunucudan-istemciye istekler (string id) öldürücü
değil, yok sayılır. Kalan işler 6d/3-4 + SSH üzerinden uzak soket bağlama (şimdilik yerel soket).

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

**Kritik bulgu (2026-07-27, strace ile):** unix soketi **WebSocket** konuşuyor. İstemci önce
HTTP Upgrade yapıyor (`GET / HTTP/1.1` + `Upgrade: websocket` → `101 Switching Protocols`),
JSON-RPC bundan sonra maskeli WS frame'leri içinde gidiyor. Yani:

| taşıma | tel biçimi |
|---|---|
| `--stdio` | düz satır-sonlu JSON |
| `--listen unix://PATH` · `ws://IP:PORT` | **WebSocket** (üstünde aynı JSON-RPC) |

Bu, kavram-gate'in "hiçbir framing yanıt vermiyor" gözlemini açıklıyor: sunucu HTTP upgrade
bekliyor, gelmeyince bağlantıyı sessizce kapatıyor. Kendi makinemde, sokete **başka hiçbir
client bağlı değilken** aynı 0-byte davranışını birebir ürettim — yani "kontrol soketi tek
sahiplidir" hipotezi yanlış. `app-server proxy` ise bizde de sessiz; ihtiyaç yok.

**Seçilen: SSH tüneli üzerinden soketle doğrudan WebSocket.** Uzak makinede hiçbir şey
çalıştırmıyoruz; SSH ile unix soketine bağlanıp WS el sıkışması yapıyoruz. Yeni port yok,
relay yok, iki tarafta da kurulum yok. Yerelde uçtan uca doğrulandı: el sıkışma → `initialize`
→ `thread/list` gerçek thread listesini döndürüyor.

Elenenler: **`app-server proxy`** (0.144/0.145'te sessiz), **`ws://` port açma**
(Tailscale/port gerektirir; soket + SSH zaten yetiyor), **`remote-control` eşleştirme**
(filo içi trafiğe üçüncü taraf relay sokar), **`codex exec resume`** (bugünkü yöntemimiz —
thread'e zorla mesaj enjekte ediyor ve çalışan otonom goal'u bölebiliyor; `turn/steer` bunun
doğrusu).

**Maliyet uyarısı:** Go stdlib'de WebSocket istemcisi yok. El sıkışma + maskeleme + frame
çözme elle yazılacak (~150 satır). Stdlib-only kuralını bozmuyor ama §6d'deki 1. maddenin
boyutunu iki katına çıkarıyor; sadeliği korumak için yalnızca metin frame'i + ping/pong
desteklensin, uzantı/parçalama desteklenmesin.

### 6d. Yapılacak işler (sırayla, MVP ölçüsünde)

1. ~~**`internal/codexrpc`**~~ **YAZILDI 2026-07-29** (`200b353`): JSON-RPC istemcisi +
   minimal WS katmanı (unix soket) + stdio taşıma. Bildirimler kanala akıyor.
2. ~~**Salt-okur gözlem**~~ **YAZILDI 2026-07-29**: `bp status`/`tree`, config'te
   `codex.sockets` varsa Codex thread'lerini gösteriyor (ad, durum, context, cwd); yüklenmemiş
   tarih tek özet satıra katlanıyor. Yazma yok.
3. **Uzak soket** — SSH tüneli üzerinden Yiğit'in soketine bağlanma (şimdilik yalnız yerel
   soket destekli); config'te `ssh:host:/path` benzeri bir adres biçimi gerekir.
4. **Mesaj teslimi** — `bp msg <thread>@codex`: tur çalışmıyorsa `turn/start`, çalışıyorsa
   `turn/steer`. Federation adreslemesiyle (`ad@peer`) aynı sözdizimi. §6e'deki koruma
   tasarlanmadan açılmaz.
5. **(Belki)** context/limit ölçümünü `account/rateLimits/read` ve `thread/tokenUsage/updated`
   ile besleyip `bp usage`'a Codex sütunu eklemek.

**Kapsam dışı (şimdilik):** thread başlatma/silme, goal yönetimi, onay akışları (`turn/steer`
dışındaki her yazma). Bunlar bp'yi Codex kokpitine çevirir — §"Reddedilenler"deki gerekçeyle
tutarsız olur.

### 6e. Mimari risk ve tek açık doğrulama

**Risk:** bp bugün "agent = tmux oturumu" varsayımı üstüne kurulu. İkinci bir arka uç eklemek,
bilinçli reddettiğimiz "her şeyi yapan kokpit" yönüne kayma riski taşır. Koruma: ince bir
**transport arayüzü**, tmux varsayılan ve tek yazma yolu olarak kalır, Codex arka ucu önce
salt-okur girer. Arayüz iki arka ucu da temiz tutamıyorsa iş durur, zorlanmaz.

**Doğrulama TAMAM (kavram-gate, 2026-07-27):** Yiğit'in *çalışan* daemon'una (0.144.0) karşı
uçtan uca çalıştı — `101 Switching Protocols`, `initialize` yanıtı
(`Codex Desktop/0.144.0 … aarch64`), ardından gerçek `thread/list` çıktısı. Yani bp, onun canlı
filosunu tmux olmadan, proxy olmadan görebiliyor. `features.code_mode_host=true` ve
`codex-code-mode-host` alt süreci ikinci istemciyi engellemiyor; soket tek sahipli değil.
Sürüm farkı (0.144.0 ↔ 0.145.0) protokolü etkilemedi.

**Yazma tarafı hâlâ kapalı:** `turn/start`, `turn/steer`, `thread/compact/start` gibi iş bölen
metotlar filo kararı ve aktif goal thread koruması olmadan çalıştırılmayacak. §6d'nin 3. maddesi
ancak o koruma tasarlandıktan sonra açılır: yazmadan önce `thread/status/changed` ile turun
durumu okunmalı, koşan otonom goal varsa `turn/start` yerine `turn/steer`, o da uygun değilse
mesaj kuyrukta beklemeli — bugünkü "meşgul agent'ı bölme" kuralının app-server karşılığı.

## 7. Goal sistemi — hiyerarşik hedef atama

**Karar (Tuna, 2026-07-31):** hiyerarşide üstte olan, altındaki agent'lara goal verebilmeli.
Henüz tasarım/implementasyon yok; bu bölüm kararı ve sınırları tutar.

**Ne:** `bp goal set <agent> "<hedef>"` / `show` / `clear` benzeri bir yüzey. Goal bir mesaj
değil **kalıcı durum**: state'te saklanır, agent'a teslim edilir ve tamamlanana/temizlenene
kadar geçerli kalır.

**Yetki kuralı (işin özü):** announce ile aynı — `fleet.IsDescendant`: bir agent yalnızca
**kendi altındakilere** goal verebilir (server-main/root her yere). Alt üste veremez, eş
seviyeler birbirine veremez. Federation üzerinden goal atama **yok**: dış mesaj taleptir,
emir değil (§3c ile tutarlı) — karşı tarafta hiyerarşi varsa onun kurallarını da biz çiğnemeyiz.

**Eldeki parçalar:**
- Claude tarafında `/goal` Stop-hook kalıbı zaten kullanılıyor (agent hedef bitmeden duramıyor) —
  teslim biçimi için doğal aday.
- Codex app-server'da birebir karşılığı hazır: `thread/goal/set|get|clear` (§6b tablosu).
  İleride aynı komutun codex arka ucu olabilir; §6e yazma guardrail'leri aynen geçerli.

**Açık sorular (tasarımda karara bağlanacak):** goal'u kim kapatır (agent "bitti" diyebilir mi,
yoksa yalnız veren mi); üst üste goal'lar (üstün üstü daha genel bir goal verirse ne olur);
teslim ve görünürlük (bar'da chip? `bp status`'ta satır?); agent kapanıp `--resume` ile
açıldığında goal'un taşınması.

---

## Reddedilenler (tekrar tartışılmasın diye)

- **Zamana dayalı otomatik compaction** — bedeli peşin, boşta agent zaten maliyetsiz.
  Yerine teslim anında compact.
- **cmux tarzı ayrı terminal emülatörü** — fazla bloat; tmux yeterli.
- **HN postu (şimdilik)** — "bir tmux orkestratörü daha" pazarı dolu ve HN yorumları acımasız
  ("overengineering", "yeni TODO app"). Post açılacaksa açı ölçüm verisi olmalı, araç değil.
- **`compact_before_task` agentbook bayrağı** — MVP'ye alınmadı.
- **`--warm-window` / `--parent` gibi ek bayraklar** — sabit varsayılanlar yeterli.
