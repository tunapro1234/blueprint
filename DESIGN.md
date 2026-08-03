# blueprint (bp) — Tasarım

Sunucudaki tüm agentic altyapıyı TEK CLI (`bp`) + TEK systemd servisi (`blueprint.service`)
altında toplayan Go projesi. Sahibi: ada (server-main). Dil: Go 1.22, tek statik binary.

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
bp status | bp tree            filo (tmux+agentbook birleşik, TREE görünüm, parent'lı)
bp open <ad> <dizin> [--parent <ad>] [--role <metin>] [--resume] [--codex] [--no-prompt]
bp close <ad>
bp msg <ad> <mesaj...>         [gönderen] zarfı; typing/busy guard; doluysa kuyruk + kanal id + qstat talimatı basar
bp q | bp qstat <kanal-id>
bp peek <ad> [n]
bp wa send [--to <hedef>] [--reply <msgId>] <mesaj...>   (outbox json'a yazar; prefix [agent])
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
| keepalive (server-main) | 2m | native: tmux has-session; yoksa `agent open` mantığı (exec bash script fallback OK) |
| usage-pulse | 10m | exec /srv/server-main/bin/usage-pulse; ardından sonuçtan bağımsız gen.py |
| usage-policy | 10m, bağımsız | exec /srv/server-main/bin/usage-policy |
| dashboard gen | pulse sonrası | exec python3 /srv/monitor/site/gen.py (pulse hatasında da çalışır) |
| usage-watch | 10m | exec /srv/server-main/bin/usage-watch |
| watch-radar | 30m | exec python3 /srv/monitor/watch/model_watch.py; ardından reactions.py |
| watch-reset | 10m | exec python3 /srv/monitor/watch/reset_watch.py |
| wa-bridge | supervisor | node /srv/whatsapp/bridge.js child process; çökerse 5s backoff ile restart |
Kurallar: işler birbirini bloklamaz (ayrı goroutine); aynı işin iki kopyası üst üste binmez
(per-iş mutex); her çalışma /srv/blueprint/state/jobs.json'a yazılır (bp service okur);
panic-recover ile daemon ölmez; SIGTERM'de wa-bridge child'a TERM iletilir.
İlk çalıştırmalar thundering-herd önlemek için kademelidir: msgq 5s, keepalive 30s,
policy 60s, pulse 90s, usage-watch 2m, reset-watch 2m30s, radar 3m.

### tmux etkileşimi (bash paritesinden birebir taşınacak KRİTİK detaylar)
- typing(): SADECE SON '❯' satırı (canlı composer); NBSP(U+00A0)+boşluk strip; doluysa gönderme.
- busy(): pane'de 'esc to interrupt'.
- send: literal send-keys (-l) / çok satırda load-buffer+paste-buffer; 0.4s; Enter; 1.2s;
  submit doğrulama (son 40 char pane'de ve busy değilse ekstra Enter).
- msg: yerel teslimde mesajın başına "[gönderen] " zarfı konur. Kimlik tmux oturum adından
  gelir (yetkili kaynak); AGENT env yalnız tmux DIŞINDA (daemon/systemd/düz kabuk) okunur.
  Agent kendi eliyle "[isim]" yazmaz — yazarsa gerçek zarfın içinde iç içe görünür.
- slash komut (/compact, /goal ...): zarfsız gider (önek komutu bozar), bunun yerine hiyerarşi
  kapısı — yalnız filo kökü ya da hedefin bir üst-atası gönderebilir, yan/yukarı reddedilir.
  '/goal <metin>' → '/goal [gönderen] <metin>' (hedefi kimin koyduğu kayda geçsin); çıplak
  /goal (sorgu/temizleme) dokunulmadan geçer.
- open: claude --dangerously-skip-permissions [-c]; resume picker'da Down+Enter (FULL, summary'ye HAYIR);
  hazır bekleme ('bypass permissions|-- INSERT --'); /rename, /remote-control, onboarding prompt;
  agentbook güncelle. --codex: codex -c model_reasoning_effort="high" + trust prompt Enter.
  --parent/--role kitaba yazılacak ebeveyni/rolü açıkça verir (yol-önekinden çıkarım yerine);
  --parent açılıştan ÖNCE filoya karşı doğrulanır, bilinmeyen adda hiçbir şey açılmaz/yazılmaz.
  Zaten açık agentta iki bayrak yalnız kitap kaydını düzeltir.
  Oturum var ama pane'de agent yok, kabuk kalmışsa: agent YERİNDE yeniden başlatılır (C-u, cd,
  komut). Pane başka bir program çalıştırıyorsa açık hata — önce bp close.
  Agent klasörü ebeveyninin klasörü altında değilse tek satır ÖNERİ basılır (asla ret);
  worktree yolları, ebeveyniyle aynı klasör ve kökün çocukları muaf.
- close: kill-session + agentbook status=closed.
- status/tree durumları: closed / idle / working / dead — dead = oturum ayakta ama pane'de agent
  yok (CLI çıkmış, kabuk kalmış); idle ile karıştırılmaz.
- Agentbook: TEK kitap — /srv/server-main/agentbook.json (+AGENTBOOK env override).
  config.json birden fazla kitap listelerse hepsi okunur, ama varsayılan tektir.
  tree görünümünde parent alanı kullanılır.

### bar (`bp bar <ad>` — pane'in tmux status-right'ı)
- Widget listesi config'ten: `bar.widgets` (varsayılan ctx, temp, queue, model, quota; talk ve
  clock kapalı). Her tick'te çağrıldığı için 45s'lik kısa ömürlü cache dosyası araya girer.
- Claude pane'inde model chip'i CANLI oturum kaydından (session jsonl'ındaki son assistant
  turu) okunur; settings/pin dosyaları yalnız effort ve fallback için — /model global
  varsayılanı değiştirip pin'e dokunmadığı için ayar dosyası iki yönde de yanılabiliyor.
- Codex pane'inde ctx ve yaş CODEX_HOME/sessions rollout kayıtlarından gelir (session_meta'daki
  cwd agent klasörüne eşlenir, mtime'a göre en taze rollout canlı olandır); model chip'i codex
  config.toml'dan. Rollout pencere bildiriyorsa ctx rengi doluluk oranına, bildirmiyorsa
  mutlak eşiklere göre.

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
- Hedef kümesi: gönderenin (tmux oturum adı / AGENT env) agentbook hiyerarşisinde ALTINDA kalan
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
