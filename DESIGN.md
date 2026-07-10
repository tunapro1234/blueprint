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
bp status | bp tree            filo (tmux+agentbook'lar birleşik, TREE görünüm, parent'lı)
bp open <ad> <dizin> [--resume] [--codex] [--no-prompt]
bp close <ad>
bp msg <ad> <mesaj...>         typing/busy guard; doluysa kuyruk + kanal id + qstat talimatı basar
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
| usage-pulse | 10m | exec /srv/server-main/bin/usage-pulse; ardından policy + gen.py ZİNCİR |
| usage-policy | pulse sonrası | exec /srv/server-main/bin/usage-policy |
| dashboard gen | pulse sonrası | exec python3 /srv/monitor/site/gen.py |
| usage-watch | 10m | exec /srv/server-main/bin/usage-watch |
| watch-radar | 30m | exec python3 /srv/monitor/watch/model_watch.py; ardından reactions.py |
| watch-reset | 10m | exec python3 /srv/monitor/watch/reset_watch.py |
| wa-bridge | supervisor | node /srv/whatsapp/bridge.js child process; çökerse 5s backoff ile restart |
Kurallar: işler birbirini bloklamaz (ayrı goroutine); aynı işin iki kopyası üst üste binmez
(per-iş mutex); her çalışma /srv/blueprint/state/jobs.json'a yazılır (bp service okur);
panic-recover ile daemon ölmez; SIGTERM'de wa-bridge child'a TERM iletilir.

### tmux etkileşimi (bash paritesinden birebir taşınacak KRİTİK detaylar)
- typing(): SADECE SON '❯' satırı (canlı composer); NBSP(U+00A0)+boşluk strip; doluysa gönderme.
- busy(): pane'de 'esc to interrupt'.
- send: literal send-keys (-l) / çok satırda load-buffer+paste-buffer; 0.4s; Enter; 1.2s;
  submit doğrulama (son 40 char pane'de ve busy değilse ekstra Enter).
- open: claude --dangerously-skip-permissions [-c]; resume picker'da Down+Enter (FULL, summary'ye HAYIR);
  hazır bekleme ('bypass permissions|-- INSERT --'); /rename, /remote-control, onboarding prompt;
  agentbook güncelle. --codex: codex -c model_reasoning_effort="high" + trust prompt Enter.
- close: kill-session + agentbook status=closed.
- Agentbook'lar: /srv/server-main/agentbook.json + /srv/probot/.orchestration/agentbook.json
  (+AGENTBOOK env override). tree görünümünde parent alanı kullanılır.

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
                              probot book AYRI kalır (alp'in) ama bp tree ikisini birleşik okur
  knowledge/                ★ model-ansiklopedisi.md, gpt56-kilavuz.md, model-prices.json
                              (eski resources/ yolları symlink)
  watch/                    ★ model_watch.py, reactions.py, reset_watch.py (+radar.json state)
                              (/srv/monitor/watch -> symlink)
  dashboard/                ★ monitor.trasumanar.ai sitesi (index.html, gen.py, data.json)
                              nginx root buraya döner; /srv/monitor/site -> symlink
                              sahibi yine server-monitor-dash agent'ı (evi güncellenir)
  accounts/ state/
```
Yeni bp komutları: `bp book [list|set <agent> <alan> <deger>]`, `bp models` (ansiklopediden
özet seçim tablosu terminale), `bp radar` (son model-radar bulguları).
Taşıma kuralı: her taşınan yol için ESKİ KONUMA SYMLINK bırakılır (hiçbir referans kırılmaz);
taşımalar cutover'la birlikte ada tarafından yapılır (Sol'a dosya taşıtılmaz).

## v1.3 — ANONS (kullanıcı, 2026-07-10)
`bp anons <mesaj...>` — hiyerarşik toplu duyuru (örn. bp toolunda değişiklik, global kural).
- Hedef kümesi: gönderenin (tmux oturum adı / AGENT env) agentbook hiyerarşisinde ALTINDA kalan
  ve şu an AÇIK olan tüm agentlar (parent zinciri takip edilir). ada(server-main) → tüm filo;
  alp(probot-main) → probot alt-ağacı. lab-* oturumları hariç.
- Mesaj otomatik "[ANONS <gönderen>] " prefix'i alır.
- Her hedef için standart msg akışı: boşsa direkt, dolu/yazılıyorsa KUYRUK (kanal id).
- Çıktı: özet tablo — "gönderildi: N, kuyruğa: M (kanal id'leri)". `bp qstat` ile takip.
- Codex oturumları da dahil (aynı send mekanizması çalışır).
