# Ortak runtime durumu ve bekçi — 5 Eylül 2026

**Host kurulumu tamamlandı:** [canlı kanıt ve son çubuk tercihi](live-rollout-2026-09-05.md).
Güncel kısa çubuk kullanım yüzdesini gösterir (`gpt` / `cc`); aşağıdaki eski
kalan yüzde örneği ve yayın engelleri önceki aşamanın kaydıdır.

Public `dist/laptop-release/` paketi yayınlandı; [host geçişi](host-rollout-2026-09-05.md)
teknik erişim nedeniyle bekliyor. Host CLI, daemon, agentbook ve
`/srv/server-main/bin/otonom-yakit-bekcisi.py` bu çalışmada güncellenmedi.

## Makine-okunur sözleşme

`bp status --json` schema 2, status/bar/mesaj geçitlerinin kullandığı
`internal/book.RuntimeFor` gözlemini sunar. Aynı status çağrısında kullanım için
ikinci bir runtime seçilmez. Ayrı çağrılar farklı anları gözlemler.

| Alan | Anlamı |
| --- | --- |
| `schema_version` | `2`; eski CLI çıktısı yeni bekçiye yeterli değildir |
| `producer` | Çıktıyı üreten CLI executable SHA-256/PID/VCS bilgisi |
| `daemon`, `daemon_verification` | Daemon başlangıç kaydı; PID başlangıç zamanı ve çalışan executable SHA doğrulaması ayrı |
| `agents[].name` | Agentbook adı; `/srv/outpost` klasörü `op-main` adını değiştirmez |
| `activity.state` | `idle`, `working`, `unknown`, `blocked`, `dead`; kapalı satırın `tmux` değeri `closed` |
| `activity.source`, `reason` | app-server/transkript/pane kanıtı ve eksik/çelişkili kanıt nedeni |
| `activity.observed_at`, `last_event_at` | Gözlem anı ile son belirleyici olayın zamanı ayrı |
| `activity.thread_id`, `binding`, `transcript_path` | Hangi konuşmanın okunduğu; bağ yoksa kullanım başka thread'den tamamlanmaz |
| `activity.screen_busy`, `turn_busy` | Ayrı kanıtlar; yokluğu `false` anlamına gelmez |
| `activity.delivery_blocked` | İş, belirsizlik veya kullanıcı taslağı nedeniyle otomatik teslim engeli |
| `usage_observed_at`, `usage_scope` | Son context ölçümünün olay zamanı ve `last_context_snapshot` kapsamı; dönem harcaması değildir |

`busy=true` emniyet kararı olabilir; tek başına çalışıyor anlamına gelmez.
Bekçiler `activity.state` ve tazeliğini kullanmalıdır. Ekranın boş olması idle
kanıtı değildir. Bilinmeyen hedefe force ile de otomatik teslim yapılmaz.
Pozitif Working görünümü iş belirtisi olabilir; aynı anda idle kaydı varsa sonuç
unknown olur. Bağlantısı kesilmiş remote istemcinin donmuş Working ekranı,
başarısız app-server sorgusunu geçersiz kılamaz.

Codex eşlemesi açık resume argv/agentbook thread pini/launch thread üzerinden,
UUID ve cwd doğrulanarak yapılır. Aynı thread'in iki agent kaydına bağlanması,
birden fazla aynı UUID rollout'u ve yarım transkript kuyruğu belirsiz sayılır.
Ortak daemon'dan miras CODEX_THREAD_ID/TMUX ve cwd'deki en yeni dosya kanıt değildir.
Claude için kayıtlı cwd içinde tekil custom-title oturumu gerekir; compact
özetleri ve metadata dokunuşları yeni iş veya yeni kullanım sayılmaz.
Açık kalmış 15 dakikadan eski turn kaydı tek başına halen çalışma kanıtı değildir;
belirleyici kapanmış turn kaydı sırf eski diye çalışan işe dönüştürülmez.

Bu eşleme runtime gözlemidir, kriptografik kimlik veya root altında saldırgana
karşı izolasyon sağlamaz. [Gönderen yetki sınırı](identity-fix.md) ayrıca geçerlidir.

## Kota çubuğu

Codex ve codex-remote, Codex usage örneğini; Claude, Claude örneğini kullanır.
`node` içindeki tanınan Codex TUI'si Claude'a düşmez. Sağlayıcı bilinmiyorsa kota
gizlenir. Örnek: %42 haftalık ve %21 kısa dönem tüketimde
`codex 7d 58% left (5h 79% left)`. Renk tüketim seviyesine göre kalır.
Bu değerler son usage örneğidir; thread context token sayısı değildir. Görünüm
cache'i 45 saniyeden 2 saniyeye indirildi; usage verisinin toplanma sıklığı değişmedi.

Claude compact_boundary içindeki postTokens, eski context ölçümünü değiştirir;
compact sonrasında tekrar yazılan eski assistant kaydı bu değeri ezmez. Compact
özeti/tool_result insan girdisi sayılmaz. Codex compact olayı yeni token_count
gelene kadar eski context ölçümünü geçersiz yapar; sıfır veya tahmin üretilmez.

## Bekçi adayı

`scripts/bp_runtime_usage.py`, schema 2 için salt-okunur tüketicidir.
`scripts/otonom-yakit-bekcisi.py` mevcut bekçinin inceleme kopyasıdır; canlı dosya
değişmedi. İki dosya aynı dizine kurulmalıdır. `--kuru` bildirim ve durum yazmaz.

- Agent adı doğrudan bp'den gelir; transcript klasör adı veya mtime taranmaz.
- 30 saniyeden eski runtime gözlemi, belirsizlik, eksik bağ/ölçüm alarm üretmez.
- Kullanım son 12 saatteki olay zamanlarından, biliniyorsa son insan girdisinden
  itibaren sayılır. Bu toplam, şu anda çalışma koşulundan ayrı denetlenir.
- Claude assistant usage aynı message ID için çift sayılmaz. Eski ağırlıklı
  eşikler korunur; gösterilen sayı assistant mesajı sayısıdır, tur sayısı değildir.
- Codex kümülatif toplamlarının farkı alınır; başlangıç değeri yoksa tüm eski
  toplam yeni harcama sayılmaz. Codex ham tokenlarına Claude ağırlıklı eşiği
  uygulanmaz: bu aday Codex ölçümünü sunar, yeni bir Codex alarm eşiği tanımlamaz.
- Okuma son 40 MiB ile sınırlıdır; kırpılma/yarım kayıt ölçümü eksik yapar.
  Kapsam dışı durumlar raporlanır, 'bütün filo temiz' sonucu çıkarılmaz.

## Canlı ve kaynak kanıtı

5 Eylül 12:44:57–58 UTC salt-okunur karşılaştırmasında eski CLI blueprint'i
idle gösterdi; aday aynı anda Working pane'ini gördü. op-main adayda unknown:
son açık turn 12:04:34 UTC, son kullanım 3 Eylül 20:26:10 UTC idi. Bunlar
ölçüm anının kanıtıdır; gelecekteki çalışma durumunu söylemez.

Canlı `/usr/local/bin/bp` ve `/srv/blueprint/bp` ayrı 2 Eylül binary kopyalarıdır.
İncelenen CLI SHA-256:
`75dfc29f5116975aebe82eb3435ba17772cf694391ccc1363cb49f157b26985c`.
Çalışan daemon executable'ı sandbox'tan doğrulanamadı; diskteki dosya onun
çalışan inode'unun kanıtı değildir. Yeni alan bu durumda açıkça unverified/unknown
der. Güncel aday checksum'ları `dist/laptop-release/checksums.txt` dosyasındadır.

Mevcut canlı kitaplarda bazı Codex thread pinleri eksiktir. Aday bu oturumların
model/tokenını başka konuşmadan seçmez; kesin idle bilinmiyorsa mesajları bekletir.
Yeni başlayan, resume UUID'si henüz kayıtlı olmayan yerel Codex için de bu sınır
geçerlidir. Eksik eşlemeler gerçek thread/pane kaydı doğrulanarak tamamlanmalıdır;
bu çalışma tüm filonun eşleştiği veya canlı mesaj tesliminin doğrulandığı iddiası değildir.

## Yayın ve doğrulama

Yetkili host kabuğunda [cutover sırası](../CODEX-CUTOVER.md) uygulanmalı:
kitapları güncel doğrulanmış thread eşlemeleriyle yedekleyip güncelle; checksum'ı
doğrulanmış adayı hem `/srv/blueprint/bp` hem `/usr/local/bin/bp` kopyasına atomik
yerleştir; yalnız `blueprint.service` durdur/başlat. CLI kopyalamak çalışan
dispatcher'ı güncellemez. Ortak Codex app-server, agentlar ve tmux scroll ayarları
değiştirilmez. Dispatcher/WA bridge kısa süre durabilir, mevcut agent işi sürer.

Sonra `bp status --json` schema 2, producer SHA ve daemon verification kontrolü;
`bp bar probot-studio-astra` içinde Codex kotası; bilinen threadlerin doğru agent
adı; unknown kuyruk nedenleri denetlenmeli. Daemon doğrulaması sandbox'ta
unverified kalırsa yetkili hostta MainPID `/proc/<pid>/exe` SHA ayrıca karşılaştırılır.
Bekçinin iki dosyası canlı dizine alınmadan önce yedeklenmeli ve yeni CLI ile
`python3 /srv/server-main/bin/otonom-yakit-bekcisi.py --kuru` çalıştırılmalı.
Cron/timer komutu değişmez; yeni bekçi süreci sonraki çağrıda dosyayı okur.
Kaynak testleri ve paket derlemesi canlı teslim veya public yayın kanıtı değildir.


## Claude effort gözlemi — 5 Eylül

Claude ana transcriptinin gerçek assistant satırındaki üst düzey `effort` alanı
`State.Effort` olarak okunur; aynı satırın modeliyle birlikte `bp status --json`
ve çubuğa gider (`fable med`, `opus high`). Sidechain ve synthetic cevaplar ana
model/effort çiftini değiştirmez. Yeni gerçek yanıtta effort yoksa önceki değeri
taşımayız; alanın bilinmeyen JSON şekli diğer token ölçümlerini düşürmez.
Gösterge son gerçek yanıtın değeridir; `/effort` sonrası yeni yanıt gelene kadar
önceki gözlem kalabilir. Başka oturumun değiştirdiği global varsayılan kullanılarak
effort uydurulmaz. Claude'un model/effort ayar değişikliklerini çalışan oturuma
uygulamak için ayrı komutlar kullanması [resmî ayar belgesinde](https://code.claude.com/docs/en/settings#when-edits-take-effect)
açıklanıyor; bu nedenle settings dosyası son çalıştırılan turun kanıtı sayılmaz.

## 2026-09-08: pinsiz embedded Codex

Fresh Codex açılışında argv/agentbook pini bulunmayabilir. Remote olmayan ve
explicit pin içermeyen oturumlar artık pane'in native Codex sürecinin kernelde
tuttuğu writer lock ile eşleşir (`activity.binding=pane-writer-lock`). Aynı
doğrulama daha önce yalnız yerel bp run oturumlarında kullanılıyordu. Thread
metadata'sı root CLI olmalı ve tek transcript bulunmalı; dosya adı/cwd/mtime tek
başına eşleme değildir. Remote veya explicit pin/conflict olan akış bu fallback'i
kullanmaz. Agentbook'a yetki/kimlik pini yazılmaz.

`bp open --thread` kaydı agentbook `launch.resumeId` alanındadır;
`identityThreadId` ayrıca desteklenir. Çalışan pane'i bp open ile tekrar açmak
eşleme tamiri değildir. `peek` görsel gözlemdir; boş ekran idle kanıtı değildir.
