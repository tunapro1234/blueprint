# bp yapılandırması

Laptop kurulumu `~/.blueprint/config.yaml` dosyasını açıklamalı olarak oluşturur.
`BP_HOME` tanımlıysa dosya o klasördedir. Mevcut dosyaya dokunulmaz; tekrar kurulum
ayarlarını, agentbook'u ve konuşma kayıtlarını korur. Dosya izni `0600` olur.

```sh
bp config path
bp config check
```

İlki kullanılan dosyanın yolunu, ikincisi geçerli olup olmadığını gösterir.
İkisi de token/şifre değerlerini dökmez. Dosya yoksa varsayılanlar kullanılır;
`bp setup` örnek dosyayı oluşturur.

## Basit örnek

```yaml
# Göreli yollar config dosyasının klasörüne göre çözülür.
agentbooks: [agentbook.json]
msgqRoot: msgq
stateDir: state
localObservation: true
bar:
  context: used
  widgets: [ctx, temp, queue, model, quota]
waBridge: false
```

Alt çubuktaki sırayı liste belirler. `ctx` context miktarı, `temp` cache tahmini
veya son ölçüm yaşı, `queue` bekleyen mesajlar, `model` model/effort, `quota`
kullanılan kotadır. `talk` son kullanıcı mesajının yaşını, `clock` saati ekler.
`widgets: []` metrik widget'larını gizler. Aktivite sembolü ayrı olarak korunur.

Yerel bp oturumlarında çubuk otomatik kurulur. Eski kurulumda yeşil varsayılan
tmux çubuğu görünüyorsa güncel installer'ı tekrar çalıştırmak açık bp
oturumlarının çubuğunu da düzeltir; agentlar kapanmaz. Güncel binary ile
`bp setup` aynı onarımı yapar. Diğer tmux oturumları ve global tuş/fare ayarları
değişmez.

## Yerel model ve context gözlemi

`localObservation: true` yerel açılışta varsayılandır. Agentbook elle hazırlanmaz;
`bp run` oluşturur ve oturumu kaydeder. `stateDir/local/` altında yalnız PID/eşleme,
model/context ölçümü ve o açılışa ait küçük ayar dosyaları tutulur (özel dizin ve
0600 dosyalar). Konuşmalar taşınmaz/kopyalanmaz: Claude kendi `projects/`, Codex
kendi `sessions/` kayıtlarını kullanır. Özel `CLAUDE_CONFIG_DIR`/`CODEX_HOME` korunur.
`stateDir: ~/.bp/state` yazarsan bp'nin küçük çalışma kayıtları o klasöre gider.

Claude'a yalnız o açılış için SessionStart gözlemi ve durum satırı okuyucusu
verilir. Mevcut `--settings`, hook'lar ve durum satırı komutunun giriş/çıkışı
korunur; global Claude ayar dosyaları değiştirilmez. Durum satırının gerçek
`context_window` girdisi kapasiteyi verir: [Claude durum verisi](https://code.claude.com/docs/en/statusline#available-data).
Codex'te pane sürecinin gerçekten tuttuğu yazıcı kilidi thread'i seçer; mevcut
JSONL kaydı model/effort/tokenları verir. En yeni dosya/cwd tahmini ve Codex hook
güvenini atlama yoktur. Linux `/proc`, macOS `ps`/`lsof` kullanılır. Bu yöntem
`thread-writer-locks` üreten Codex sürümlerini gerektirir; sunucudaki 0.153.4'te
salt okunur canlı doğrulama yapıldı. Açıkça `--remote` ile açılan Codex mevcut
remote gözlem yolunda kalır; yeni uzak daemon kurulmaz.

Sunucu ve laptopta `bar.context: used` varsayılandır. İsteğe bağlı `remaining`: `170k boş` raporlanan pencereye
göre kalan miktardır; otomatik compact eşiği farklı olabilir. `used` kullanılan
miktarı gösterir. Ölçüm henüz gelmediyse `ctx —`, yalnız kullanılan miktar biliniyor
ama pencere bilinmiyorsa `30k/?` görünür. Kapasite uydurulmaz; Codex'in ilk
model/token ölçümü ilk turdan sonra gelebilir. Compact/resume bildirimi eski
context ölçümünü tazelemez. Kota göstergesi bundan ayrı ve kullanılan yüzdedir;
laptopta kota kaynağı yoksa sahte limit yazılmaz.

Eski sürümle açık kalan oturuma yeni CLI başlangıç ayarı enjekte edilmez.
Installer çubuğu onarır ve gerekirse sonraki açılış için bilgi verir. Konuşmayı
koruyarak `claude --resume` / `codex resume` ile seçip yeniden açınca otomatik
eşleme devreye girer. `localObservation: false` sonraki açılışlar için gözlemi
kapatır. Bu eşleme gönderen yetkisi/hiyerarşi yetkisi vermez.

## Diğer mevcut ayarlar

| Anahtar | İşlev |
| --- | --- |
| `agentbooks` | Bağlayıcı agentbook dosyaları |
| `tokenAgentbooks` | Token raporlarının defterleri; laptopta varsayılanı agentbooks |
| `msgqRoot`, `stateDir` | Mesaj kuyruğu ve yerel çalışma kayıtları |
| `usageHistory`, `usageBin` | Mevcut kota verileri ve yardımcı program klasörü |
| `clipboardDir` | Clipboard dosyaları |
| `waBridge`, `waOutbox`, `waStore` | Mevcut WhatsApp entegrasyonu |
| `codex.sockets` | Salt okunur Codex app-server gözlem soketleri |
| `ntfy.url`, `ntfy.topic`, `ntfy.token` | Mevcut bildirim entegrasyonu |
| `fed.mode`, `fed.listen`, `fed.hub`, `fed.peerName`, `fed.token`, `fed.expose` | Mevcut federasyon ayarları |

İsteğe bağlı entegrasyonlar laptopta kapalıdır. Alanlar mevcut JSON ayarlarıyla
aynı isimleri taşır; YAML kendi başına yeni servis/bağlantı kurmaz. `~/` ev
klasörüne açılır. `$VAR`, komut çalıştırma veya gizli config birleştirmesi yoktur.
Federasyonun loopback/TLS/token denetimleri YAML'da da geçerlidir.

## Uyumluluk ve değişikliklerin uygulanması

`config.yaml`, `config.yml` ve eski `config.json` desteklenir; aynı klasörde
birden fazlası varsa bp hata verir. JSON kurulumu yeniden çalıştırıldığında
üzerine YAML oluşturulmaz. JSON'dan geçerken önce bir kopya/yedek ayırıp tek
etkin dosya bırakılmalıdır. Sunucunun mevcut JSON dosyası bu yayında korunur.

YAML'da bilinmeyen anahtarlar, yinelenen anahtarlar, yanlış tipler ve birden
fazla belge reddedilir. Bozuk YAML ile varsayılanlara dönüp işlem yapılmaz.
Eski JSON yükleme davranışı korunur; `bp config check` bozuk JSON'u da reddeder.

Yeni CLI çağrıları ve alt çubuk ayarları dosyayı yeniden okur. Önceden açılmış
uzun ömürlü daemon/yerel worker kendi config kopyasını tutar; onların ayarları
sonraki doğal açılışta veya uygun zamanda yapılan servis yenilemesinde değişir.
Bu özellik agent/model/effort veya tmux scroll ayarlarını değiştirmez.

Mosh uyanıklık kontrolü ve Claude'a otomatik özet mesajları henüz uygulanmadı.

## Yerel fare ve dış uygulamalara renk

`localMouse: true` (varsayılan), yalnız `bp run` oturumlarında tmux fare
kaydırmasını ve seçimini açar. `false` kapatır. `bp setup --shell bash` veya
`bp setup --shell zsh`, aynı BP_HOME içindeki açık yerel oturumlara da uygular;
agentı yeniden başlatmaz. Global tmux ayarları, prefix ve kullanıcının tuş
bağlantıları korunur. Sistem panosuna aktarım terminalin clipboard desteğine bağlıdır.

`bp color <agent>` alt çubuğun agent rengini `#rrggbb` olarak verir.
`bp color <agent> --json` agent, index (0–255) ve hex alanlarını verir.
Canlı Claude renk plakasından, yoksa kayıtlı renkten, o da yoksa bp varsayılanından
okur. İlk 16 ANSI rengin HEX karşılığı xterm varsayılanıdır; özel terminal paleti
bunları farklı gösterebilir. Komut window borderını değiştirmez: window yöneticisi
window–agent eşlemesini yapıp bu rengi uygulamalıdır. Renk bir kimlik/yetki kanıtı değildir.

`bp color <agent> blue` her harness için kalıcı bp rengi seçer; mevcut Claude
renginden önceliklidir. `auto` bu seçimi kaldırır ve native/kayıtlı renge döner.
Renkler: red, orange, yellow, green, cyan, blue, purple, pink, gray, white;
0–255 xterm indeksi de kabul edilir. Alt çubuk sonraki yenilemede değişir.
Seçim agent adına bağlıdır; sabit yerel ad için `bp run --name work codex` kullanılır.

## Bilgisayarın varsayılan agent rengi

```yaml
bar:
  context: used
  defaultColor: purple
```

Mevcut `bar` bölümüne ekleyin, ikinci bir `bar` oluşturmayın. Öncelik:
agenta özel `bp color` → `bar.defaultColor` → native/kayıtlı renk → bp varsayılanı.
Alanı kaldırmak veya boş string yapmak eski otomatik davranışı geri getirir.
Renk adları ve 0–255 indeksleri desteklenir. Açık oturumların sonraki bar
yenilemesinde uygulanır; agentın modeli veya kendi TUI teması değişmez.

## Compact sonrası teslim

Manuel compact tamamlanınca `compact_boundary` (trigger=manual), compact özeti
ve native `Compacted` komut çıktısı birlikte tur kapanışını doğrular. Otomatik
compact kapanış değildir. Yeni gerçek tur, eksik kayıt ve belirsiz durum teslimi
engellemeye devam eder. Ek observer hook gerekmez; mevcut yerel oturumlar yeni
bp mesaj/worker sürümüyle bu kayıtları okuyabilir.

`bp q --retry`, yeni mesaj oluşturmadan mevcut kuyruğu güncel binary ile bir
kez işler. Meşgul/draft/kimlik kontrollerini atlamaz. Güncelleme öncesinden
kalan local worker eski binary ile çalışıyorsa, agentı kapatmadan bu yol kullanılır.

## Native /rename ve canlı kimliğin gösterimi

Claude içindeki `/rename orch`, doğrulanmış transcript üzerinden izlenir.
Codex `/rename` adı da bağlı thread UUID ile native `session_index.jsonl`
kaydından okunur; cwd veya ekran metni kimlik kanıtı sayılmaz.
`bp name` alt çubuğu yeni adı gösterir; `bp status --json` sabit `name` yanında
`display_name` verir. `bp msg orch`, `bp peek orch` ve `bp color orch` tek bir canlı
oturum eşleşiyorsa bu adı kabul eder. İki aynı başlıkta hata verilir. Canonical
agent adları başlıklardan önceliklidir; korunan orkestratör ve başka kayıtlı agent adına
bürünmek display metadata üzerinden mümkün değildir.

Tmux oturum adı, kuyruk hedefi, parent ve authority kimliği sabit kalır. Başlık
gözlemi küçük `nativeTitle` metadata'sına kaydedilir; rename kaydı transcript
kuyruğundan uzaklaşınca eski başlığa dönülmez. User/tool metninde geçen isimler
ve sidechain kayıtları başlık sayılmaz. Güncelleme yeni prompt/hook gerektirmez.

Embedded Codex'te canlı writer lock; Claude'da doğrulanmış süreç session id'si
varsa telemetry eski launch pininden bağımsız okunur. Bu gözlem identityThreadId
yetki pinini değiştirmez. Remote Codex eşleme/pin kontrolleri korunur.


## Yerel Claude continue / resume sahipliği

`claude -c` / `--continue`, en son yerel konuşmanın UUID'sini bir kez çözer ve
Claude'u açık UUID ile başlatır. Aynı UUID zaten canlı bir bp pane'ine bağlıysa
o pane'e attach olur; yeni süreç, mesaj veya agent kaydı oluşturulmaz. Bu durumda
yeni komuttaki model/izin/prompt argümanları açık sürece uygulanmaz. Kapanmış
konuşmanın önceki agent adı tekrar kullanılır. Symlink cwd'ler fiziksel yola
normalize edilir; eski logical-path transcript dizini de continue aramasına katılır.

`stateDir/local-resume/` kilidi, sahiplik kontrolü ile tmux oluşturmayı tek işlem
olarak sıralar. Kalıcı claim, `_session` kaydı oluşana kadarki boşluğu kapatır;
canlı PID doğrulamalı gözlem varsa önceliklidir. Bir UUID'nin birden fazla canlı
eski sahibi varsa bp isimlerini gösterip durur; pane kapatmaz veya kazanan seçmez.
Eşit güncellikte farklı konuşmalar da sessizce seçilmez.

Bu koruma aynı BP_HOME ve tmux sunucusundaki `bp run claude -c` ve UUID'li
`--resume` içindir. UUID'siz `claude --resume` / `-r`, `codex resume` ve başlık
sorguları native CLI'ye değiştirilmeden aktarılır. Kendi resume ekranını CLI
çizer; bp numaralı liste üretmez, seçim tuşlarını okumaz. Konuşma seçilene kadar
bp geçici pane'i bilir fakat thread/model uydurmaz ve mesaj teslim etmez. Seçimden
sonra mevcut native callback/writer-lock gözlemi konuşmayı bağlar.

Native seçicide açık bir konuşma seçilmesi, yukarıdaki açılış öncesi attach
korumasından geçmez; aktif writer kontrolü ve uyarısı native CLI'ye aittir.
Mevcut writer kapatılmaz veya kilidi kaldırılmaz. `--fork-session` bilinçli yeni konuşma
olduğu için yeniden bağlanmaz. BP dışındaki Claude süreçlerini veya TUI içinden
sonradan `/resume` ile başka konuşmaya geçişi kilitlemez. Eski kayıt/transkriptler
silinmez; var olan çoklu writer'lar otomatik olarak birleştirilmez.

## Güncelleme bildirimi

`updateCheck: true` yerel interaktif açılışlarda günde en fazla bir arka plan
kontrolünü etkinleştirir. Hazır önbellekte yeni sürüm varsa terminalde kısa
bildirim çıkar; agent mesajı veya model çağrısı oluşturulmaz. `false` bunu kapatır.
`bp update --check --json` elle, makine okunur kontrol sağlar.

Machine mode is explicit: `BP_HOME` selects a configuration directory. Without it,
bp reads an optional `/etc/blueprint/home` selector written by `install.sh --server`;
otherwise it uses `~/.blueprint`. A checkout at `/srv/blueprint` alone does not
activate server integrations. Local installs are the default; `--client` and
`--server` must be selected explicitly. On a configured server, set `BP_HOME` to
your personal configuration directory to use a separate local installation.


## Konuşmayı koruyarak çalışma klasörünü değiştirmek

Bu bölüm inceleme sonucudur; BP henüz bir `move` komutu sunmuyor ve otomatik
klasör/transcript taşıması yapmıyor.

- Codex, belirli UUID ile `resume` ve `--cd` kabul ediyor. `tui.resume_cwd`
  `current` veya `session` seçebiliyor; ayar yoksa dizinler farklı olduğunda native
  seçim gösteriliyor. Aynı konuşmayı devam ettirmek için `fork` kullanılmamalı.
  [CLI referansı](https://developers.openai.com/codex/cli/reference/) ve
  [config referansı](https://developers.openai.com/codex/config-reference/).
- Claude, UUID ile resume sırasında güncel sürümlerde diğer proje kayıtlarını da
  arıyor (belgelenen eşik: 2.1.223). Eski proje dizini artık yoksa native picker
  mevcut dizinde devam edebiliyor; başka mevcut projeden seçim ise bir `cd`/resume
  komutu sunabiliyor. Dolayısıyla UUID'nin bulunması tek başına yeni cwd'nin
  uygulandığını kanıtlamaz.
  [Claude resume](https://code.claude.com/docs/en/cli-reference) ve
  [session davranışı](https://code.claude.com/docs/en/sessions).

BP entegrasyonu için önerilen akış: önce eski/yeni fiziksel yolları ve tam thread
kimliğini gösteren bir önizleme; doğal CLI kapanışından sonra aynı UUID ile native
resume; callback/app-server üzerinden yeni cwd ve aynı thread kanıtı; ancak bundan
sonra agentbook/local runtime kaydını güncelleme. Model, effort, servis tier ve
izolasyon korunmalı. Başarısızlıkta eski kayıt ve dosyalar yerinde kalmalı.

Kodda ek çalışma gerekiyor: `bp open --resume` ve yerel observation doğrulaması
cwd'yi mevcut kayıtla eşleştiriyor. Yeni cwd'ye geçişin doğrudan metadata değiştirerek
bu denetimi atlaması doğru olmaz. Remote Codex'te yürütücü TUI değil app-server;
loaded thread ve writer sahipliği ayrıca doğrulanmalı. Proje dosyalarını taşımak,
konuşmanın çalışma dizinini değiştirmekten ayrı ve açıkça seçilen bir işlem olmalı.
Şu aşamada gerçek kullanıcı konuşmasıyla taşıma testi yapılmadı.

## Local Codex launch and sandbox diagnostics

For new local `bp open` registrations, an explicit `--parent` wins; otherwise
BP uses the verified registered caller, or the local coordinator when the caller
is unverified. Sharing a folder with another agent never selects the parent.
Reopening an existing registration preserves its parent unless explicitly changed.

Local `bp open --codex` (including explicit resume) uses the same session worker
and bar setup as `bp run codex`. The managed role and parent stay intact. Codex
observations come from the native writer lock and transcript; an observation path
in the book does not imply that Codex writes a Claude-style `observation.json`.
Existing running panes are not replaced by an update. Remote and legacy server
launches retain their existing execution boundaries.

If `bp doctor` reports `tmux_access`, run it from the outer terminal as well.
A denied socket is not evidence that the agent exited. Codex's command sandbox
can deny tmux access while the worker outside that sandbox still updates the bar.
BP does not automatically disable the sandbox or allow the raw tmux socket:
that socket can execute host commands, so it is broader than a status/message
interface. See [Codex Unix socket permissions](https://learn.chatgpt.com/docs/permissions#unix-sockets).
A readable agent label or running worker alone also does not grant sender
authority; missing per-execution thread evidence remains explicitly unverified.
