# blueprint (bp) — Tasarım

Sunucudaki tüm agentic altyapıyı TEK CLI (`bp`) + TEK systemd servisi (`blueprint.service`)
altında toplayan Go projesi. Sahibi: blueprint; ust yetkili server-main. Dil: Go 1.22, tek statik binary.

## Neden / ne birleşiyor
Şu anki dağınıklık: 7 timer (usage-pulse, usage-policy, usage-watch, agent-msgq,
server-main-keepalive, monitor-watch-radar, monitor-watch-reset) + 1 daemon (wa-bridge) +
2 bash CLI (`agent`, `wa`). Hepsi → `bp` + `blueprint.service`.

## Mimari ilke: SUPERVISOR, REWRITE DEĞİL (v1)
Periyodik işlerin savaş görmüş script'leri (python/node) YERİNDE KALIR; Go daemon bunları
zamanlayıp (goroutine + ticker) subprocess olarak çalıştırır ve sağlığını izler. Native Go'ya
sadece CLI tarafı + mesaj kuyruğu (channel'larla) taşınır. Böylece geçiş riski düşük, tek
servis hedefi sağlanır; v2'de istenirse iş mantığı Go'ya taşınır.

## Binary: /srv/blueprint/bp  (symlink: /usr/local/bin/bp)

### CLI komutları (v1 — bash `agent`+`wa` paritesi şart)
```
bp status [--json] | bp tree    filo (tmux+agentbook birleşik, TREE görünüm, parent'lı)
bp open <ad> <dizin> [--parent <ad>] [--role <metin>] [--resume] [--codex|--claude|--hermes] [--remote unix://] [--thread UUID] [--no-sandbox] [--no-prompt]
bp close <ad>
bp msg <ad> <mesaj...>         [gönderen] zarfı; typing/busy guard; doluysa kuyruk + kanal id + qstat talimatı basar
bp compact [--idle-hours S] [--min-ctx N] [--apply]      politika seçimi; bayraksız hali LİSTELER
bp compact --all [--min-age <dk>] [--exclude <ad,...>] [--apply]   gönderenin tüm alt ağacı
bp q | bp qstat <kanal-id>
bp peek <ad> [n]
bp wa send [--to <hedef>] [--reply <msgId>] [--from <etiket>] <mesaj...>   (outbox json'a yazar; prefix [gönderen])
                               kimlik internal/identity + book.ThreadIdentity ile doğrulanır;
                               Codex thread'i explicit identityThreadId kaydına bağlanır.
                               Ortak daemon TMUX mirası veya AGENT env yetki kanıtı değildir.
                               Eşleşmeyen çağrı bilinmiyor/codex?: olarak görünür.
bp wa read <hedef> [n] | bp wa chats
bp usage                       history.jsonl son durum (5h/7d/fable/codex + resetler)
bp policy status|override <saat>
bp service                     daemon iş sağlığı (son çalışma, durum, sonraki çalışma)
bp daemon                      (servis modu — systemd bunu çalıştırır)
```

### Daemon işleri (goroutine'ler; tek process)
| iş | aralık | yöntem |
|---|---|---|
| msgq dispatcher | 30s | NATIVE Go (channel tabanlı; pending/ dizini kaynak, done/'a kayıt) |
| keepalive (filo koku) | 2m | kayitli launch/cwd ve Codex thread kimligiyle ayni konusmayi acar; eksik kayitta tahmin etmez |
| usage-pulse | 10m | exec /srv/server-main/bin/usage-pulse; ardından sonuçtan bağımsız gen.py |
| dashboard gen | pulse sonrası | exec python3 /srv/monitor/site/gen.py (pulse hatasında da çalışır) |
| usage-watch | 10m | exec /srv/server-main/bin/usage-watch |
| watch-radar | 30m | exec python3 /srv/monitor/watch/model_watch.py; ardından reactions.py |
| watch-reset | 10m | exec python3 /srv/monitor/watch/reset_watch.py |
| wa-bridge | supervisor | node /srv/whatsapp/bridge.js child process; çökerse 5s backoff ile restart |
Kurallar: işler birbirini bloklamaz (ayrı goroutine); aynı işin iki kopyası üst üste binmez
(per-iş mutex); her çalışma /srv/blueprint/state/jobs.json'a yazılır (bp service okur);
panic-recover ile daemon ölmez; SIGTERM'de wa-bridge child'a TERM iletilir.
İlk çalıştırmalar thundering-herd önlemek için kademelidir: msgq 5s, keepalive 30s,
pulse 90s, usage-watch 2m, reset-watch 2m30s, radar 3m.

### tmux etkileşimi (bash paritesinden birebir taşınacak KRİTİK detaylar)
- typing(): harness'a gore tum canli composer kutusu okunur; yabanci metin varsa dokunulmaz.
- busy(): ekran gecidi + transcript turu; remote Codex icin app-server thread durumu. Belirsiz remote durumu normal teslimi engeller.
- send: bütün harnesslarda load-buffer + bracketed paste (-p); composer bütünlüğü, kullanıcı aktivitesi ve submit doğrulaması. Vim Normal/Insert için Escape/i gönderilmez.
- msg: yerel teslimde mesajın başına "[gönderen] " zarfı konur. Kimlik doğrulanmış çağıran pane veya kayıtlı Codex thread
  eşlemesinden gelir. AGENT bildirimi belirsiz etiket taşır, yetki sağlamaz.
  Agent kendi eliyle "[isim]" yazmaz — yazarsa gerçek zarfın içinde iç içe görünür.
- slash komut (/compact, /goal ...): zarfsız gider (önek komutu bozar), bunun yerine hiyerarşi
  kapısı — yalnız filo kökü ya da hedefin bir üst-atası gönderebilir, yan/yukarı reddedilir.
  '/goal <metin>' → '/goal [gönderen] <metin>' (hedefi kimin koyduğu kayda geçsin); çıplak
  /goal (sorgu/temizleme) dokunulmadan geçer.
- compact: varsayılan seçici POLİTİKA — pane'i claude olan, açık, meşgul olmayan, son gerçek
  insan turu --idle-hours'tan (24sa) eski ve context'i --min-ctx'ten (200k) büyük agentlar;
  server-main asla hedef değil. Bayraksız çalıştırma yalnız karar tablosu basar
  (AGENT/KONUSMA/CONTEXT/KARAR) ve hiçbir şey göndermez; göndermek için --apply.
  --dry-run ve --policy varsayılanın eşanlamlısı (uyumluluk; yardım metninde geçmez);
  çelişkili çiftler ayrıştırmada reddedilir: --apply+--dry-run ve --all+--policy.
  --all = eşiklere bakmayan kaba süpürme: gönderenin tüm alt ağacı (--exclude, --min-age).
  state/compact.json bastırma penceresi (--min-age, varsayılan 30dk) her iki seçicide de geçerli.
  --apply her gönderimden hemen önce pane'i yeniden okur; meşgul hedef KUYRUĞA ALINMADAN atlanır
  (geç düşen /compact yanlış konuşmayı sıkıştırır). Okunamayan pane meşgul sayılır.
- open: yeni kayitlarda varsayilan Codex; --claude/--hermes acik secimdir.
  Codex resume UUID ile ayni thread'i acar; cwd eslesmesi zorunlu, model/effort/tier override edilmez.
  --remote unix:// yalniz acik --no-sandbox ile kabul edilir: TUI bwrap'i daemonu izole etmez.
  Kayitli launch tekrar acilista korunur; hazirlik timeout'u hata olarak bildirilir.
  Claude dalinda /rename ve /remote-control akisi korunur.
  --parent/--role kitaba yazılacak ebeveyni/rolü açıkça verir (yol-önekinden çıkarım yerine);
  --parent açılıştan ÖNCE filoya karşı doğrulanır, bilinmeyen adda hiçbir şey açılmaz/yazılmaz.
  Zaten açık agentta iki bayrak yalnız kitap kaydını düzeltir.
  Oturum var ama pane'de agent yok, kabuk kalmışsa: agent YERİNDE yeniden başlatılır (C-u, cd,
  komut). Pane başka bir program çalıştırıyorsa açık hata — önce bp close.
  Agent klasörü ebeveyninin klasörü altında değilse tek satır ÖNERİ basılır (asla ret);
  worktree yolları, ebeveyniyle aynı klasör ve kökün çocukları muaf.
- close: kill-session + agentbook status=closed.
- status/tree durumları: closed / idle / working / unknown / blocked / dead — dead = oturum ayakta ama pane'de agent
  yok (CLI çıkmış, kabuk kalmış); idle ile karıştırılmaz.
- status --json: schema_version=2, producer/daemon executable kimliği ve activity kanıtları;
  [runtime sözleşmesi](docs/runtime-status.md). Her agent için name, tmux, status, folder, parent; bilindiğinde
  ctx_tokens, cache_age_seconds, last_human_age_seconds, model (bilinmeyen sayı sıfır değil, yok).
- -h/--help komut mantığından ÖNCE yanıtlanır; msg/announce/wa'da yalnız baştaki bayraklar taranır,
  serbest metindeki --help mesaj olarak gider. status/tree/open/close/msg/peek '-' ile başlayan
  argümanı agent adı sanmaz, hata verir.
- Agentbook: TEK kitap — /srv/server-main/agentbook.json (+AGENTBOOK env override).
  config.json birden fazla kitap listelerse hepsi okunur, ama varsayılan tektir.
  tree görünümünde parent alanı kullanılır.

### bar (`bp bar <ad>` — pane'in tmux status-right'ı)
- Widget listesi config'ten: `bar.widgets` (varsayılan ctx, temp, queue, model, quota; talk ve
  clock kapalı). Her tick'te çağrıldığı için 2s'lik kısa ömürlü cache dosyası araya girer.
- Canlı model chip'i eşleşmiş runtime kaydından gelir; bilinmeyen model config'ten uydurulmaz.
  Kota observed runtime sağlayıcısınındır: Codex/remote için Codex, Claude için Claude.
  Yüzdeler kullanılan limittir; sağlayıcı bilinmiyorsa kota gizlenir.
- Cache sıcaklığı sabit 1 saat değildir: son Claude kullanımındaki açık 5m/1h yazım
  kaydından tahmin edilir (`warm~`/`cold~`). TTL bilinmiyorsa yalnız `age` gösterilir.
  `cache_age_seconds` uyumluluk için son token ölçümünün yaşıdır; TTL değildir.
  JSON'daki `cache_ttl_seconds`/`cache_estimate` yalnız TTL kanıtı varsa eklenir.
- Claude/Codex status/bar/teslim gecitleri internal/book.RuntimeFor kullanir. Codex thread UUID
  acikca eslesmelidir; cwd/mtime fallback yoktur. Token/model kayitlari
  buyuk tool ciktisi arkasinda da bagimsiz okunur; olcum yasi dosya mtime'i ile sifirlanmaz.
  Yerel app-server salt-okunur thread/read ile yuklu thread'in durum/model/effort bilgisini
  verir. Resume/turn/start cagrilmaz. Bilinmeyen/celiskili durum teslimi bekletir;
  busy emniyet bool'u working diye gosterilmez. Claude eslemesi cwd + tekil oturum adidir.
- Otomatik usage-policy model degisimi kaldirildi; bp policy status/override manuel kalir.
- Pending mesajlar yas/adet nedeniyle budanmaz; teslim snapshot'i arsivlenerek onaylanir,
  eszamanli eklenen mesajlar korunur. Msgq history silinmez; kapali hedefin mesaji bekler.

### Yapı
```
/srv/blueprint/
  cmd/bp/main.go        (cobra YOK - stdlib flag yeter, bağımlılık minimal)
  internal/tmux/        (typing/busy/send/peek/open/close)
  internal/msgq/        (channel tabanlı kuyruk + dispatcher)
  internal/book/        (agentbook oku/yaz)
  internal/daemon/      (scheduler + supervisor + jobs.json state)
  internal/wa/          (outbox yazıcı + store okuyucu)
  internal/usagecli/    (history.jsonl okuyucu + policy state)
  state/                (jobs.json, runtime)
  DESIGN.md  README.md  Makefile
```

## Geçiş planı (SIRALI, riskli adım en sonda)
1. Build + `bp` CLI'ın salt-okur komutlarını doğrula (status/tree/usage/q/peek).
2. `bp msg` canlı test (kuyruk dahil). bash `agent` PARALEL kalır (bozulmaz).
3. `bp daemon`ı FOREGROUND test et (eski timerlar açıkken çakışma olmasın diye önce
   timerları durdurup 15 dk gözle; sorun olursa timerları geri aç).
4. blueprint.service enable; 7 timer + wa-bridge.service disable (dosyalar silinmez, disable).
5. /usr/local/bin/agent ve wa → bp'ye yönlendiren uyumluluk shim'i (eski çağıranlar kırılmasın).
6. 24 saat gözlem; sorun yoksa eski unit dosyaları arşive.

## İş bölümü
- ada (Fable): tasarım (bu doküman), entegrasyon kararları, cutover.
- Sol (gpt-5.6-sol, codex): Go implementasyonu (bu spec'e göre), build+birim test.
- Opus (subagent): kod review — özellikle tmux etkileşim paritesi, race'ler, migration checklist.

## v1.1 — ÇOKLU HESAP YÖNETİMİ (plan, kullanıcı istegi 2026-07-10)
Amaç: birden fazla Claude (ve Codex) aboneliğini tek filoda yönetmek — limit dolunca
agent'ları başka hesaba kaydırabilmek.

### Mekanizma (per-agent hesap)
- Claude Code: `CLAUDE_CONFIG_DIR=<dizin>` env'i ile farklı credential/config seti kullanır.
- Codex: `CODEX_HOME=<dizin>` ile aynı şekilde.
- Hesap deposu: `/srv/blueprint/accounts/{claude,codex}/<hesap-adı>/` (her biri tam config dizini;
  chmod 700). `default` = mevcut /root/.claude ve /root/.codex (symlink ya da kayıt).
- `bp open` yeni bayrak: `--account <ad>` → tmux oturumu ilgili env ile başlar. Agentbook'a
  `account` alanı yazılır; `bp tree` hesap etiketi gösterir.

### bp komutları
```
bp account list                     hesaplar + her birinin canlı usage yüzdeleri
bp account add claude <ad>          dizini hazırlar; kullanıcı 'bp account login <ad>' ile
bp account login <ad>               interaktif girişi kendi terminalinde yapar (CLAUDE_CONFIG_DIR set edilmiş claude açar)
bp account assign <agent> <ad>      agent'ı sonraki açılışta o hesaba bağlar (book'a yazar)
```

### usage-pulse / policy entegrasyonu
- pulse TÜM claude hesaplarının OAuth usage endpoint'ini gezer → history satırına
  `accounts:{<ad>:{5h,7d,fable_7d}}` ekler; dashboard hesap-bazlı gösterir.
- policy v2: hesap-bazlı state; bir hesabın haftalığı eşiği aşarsa YENİ açılan agent'lar
  otomatik diğer hesaba yönlenir (açık oturum taşınmaz — restart gerektirir, o MANUEL/onaylı).
- WhatsApp'a "hesap A doldu, yeni agentlar B'den açılıyor" bildirimi.

### Sıra
Cutover (v1) bitip 24h stabil olduktan sonra v1.1 başlar. İlk adım: kullanıcı ikinci hesabın
girişini yapar (bp account add+login), sonra pulse çoklu-hesap, sonra policy yönlendirme.

## v1.2 — KAPSAM GENİŞLEMESİ: blueprint her şeyin evi (kullanıcı, 2026-07-10)
Agentbook, ansiklopedi/bilgi tabanı, model-araştırma watcherları VE monitor dashboard'ın kendisi
blueprint çatısına taşınır. Hedef yerleşim:
```
/srv/blueprint/
  cmd/ internal/            Go kaynak
  bp                        binary
  book/agentbook.json       ★ agentbook (canonical; eski yollar symlink):
                              /srv/server-main/agentbook.json -> buraya
                              filo tek kitapta birleşti; alt kitaplar okunmuyor
  knowledge/                ★ model-ansiklopedisi.md, gpt56-kilavuz.md, model-prices.json
                              (eski resources/ yolları symlink)
  watch/                    ★ model_watch.py, reactions.py, reset_watch.py (+radar.json state)
                              (/srv/monitor/watch -> symlink)
  dashboard/                ★ monitor.tunapro.xyz sitesi (index.html, gen.py, data.json)
                              nginx root buraya döner; /srv/monitor/site -> symlink
                              sahibi yine server-monitor-dash agent'ı (evi güncellenir)
  accounts/ state/
```
Yeni bp komutları: `bp book [list|set <agent> <alan> <deger>]`, `bp models` (ansiklopediden
özet seçim tablosu terminale), `bp radar` (son model-radar bulguları).
Taşıma kuralı: her taşınan yol için ESKİ KONUMA SYMLINK bırakılır (hiçbir referans kırılmaz);
taşımalar cutover'la birlikte ada tarafından yapılır (Sol'a dosya taşıtılmaz).

## DİL KURALI (kullanıcı, 2026-07-10): bp TAMAMEN İNGİLİZCE
Komut adları, flag'ler, yardım metni, TÜM çıktı ve kod içi isimlendirme İngilizce olacak
(tree'deki "bosta/kapali" gibi Türkçe çıktılar da İngilizce'ye çevrilecek: idle/closed/working).
Agentlara giden MESAJ İÇERİĞİ göndericiye aittir (o Türkçe olabilir) — araç dili İngilizce.

## v1.3 — ANNOUNCE (kullanıcı, 2026-07-10)
`bp announce <message...>` — hiyerarşik toplu duyuru (örn. bp toolunda değişiklik, global kural).
- Hedef kümesi: doğrulanmış ana agentın (pane/thread kimliği) agentbook hiyerarşisinde ALTINDA kalan
  ve şu an AÇIK olan tüm agentlar (parent zinciri takip edilir). ada(server-main) → tüm filo;
  alp(probot-main) → probot alt-ağacı. lab-* oturumları hariç.
- Mesaj otomatik "[ANNOUNCE <sender>] " prefix'i alır.
- Her hedef için standart msg akışı: boşsa direkt, dolu/yazılıyorsa KUYRUK (kanal id).
- Çıktı (İngilizce): "sent: N, queued: M (channel ids)". `bp qstat` ile takip.
- Codex oturumları da dahil (aynı send mekanizması çalışır).

## v1.6 — WORKTREE DESTEĞİ (kullanıcı, 2026-07-10)
Amaç: farklı agentların AYNI repoda çakışmadan paralel çalışması (probot-studio .worktrees
kalıbının genelleşmesi). bp genel/açık bir agent yönetim sistemi olarak bunu birinci sınıf yapar.
```
bp worktree add <repo-dizin> <konu>     -> <repo>/.worktrees/<konu> olusturur (git worktree add
                                           -B <konu>/dev), yolu basar
bp worktree list <repo-dizin>           -> mevcut worktree'ler + hangi agent icinde (tmux eslesme)
bp worktree rm <repo-dizin> <konu>      -> worktree'yi kaldirir (dirty ise reddeder, --force ile)
bp open <ad> <repo-dizin> --worktree <konu>  -> worktree yoksa olusturur, agent'i ICINDE acar
```
- Dal adlandirma: <konu>/dev (mevcut kalipla ayni: shop/dev, builder/dev...).
- bp tree, worktree'de calisan agentin yanina repo+dal etiketi gosterir.
