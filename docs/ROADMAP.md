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

---

## Reddedilenler (tekrar tartışılmasın diye)

- **Zamana dayalı otomatik compaction** — bedeli peşin, boşta agent zaten maliyetsiz.
  Yerine teslim anında compact.
- **cmux tarzı ayrı terminal emülatörü** — fazla bloat; tmux yeterli.
- **HN postu (şimdilik)** — "bir tmux orkestratörü daha" pazarı dolu ve HN yorumları acımasız
  ("overengineering", "yeni TODO app"). Post açılacaksa açı ölçüm verisi olmalı, araç değil.
- **`compact_before_task` agentbook bayrağı** — MVP'ye alınmadı.
- **`--warm-window` / `--parent` gibi ek bayraklar** — sabit varsayılanlar yeterli.
