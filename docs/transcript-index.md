# Transcript analiz katmanı — tasarım planı

Durum: **plan**, kod yok. İstek: server-main/ada (Tuna), 2026-08-07.
Ölçümler bu makinede tam tarama ile alındı (örnekleme değil; aksi belirtilen yerler hariç).

## Amaç

bp bugün "kim ne kadar token yaktı"yı biliyor (`internal/tokens`), "agent ne YAPTI"yı bilmiyor.
Eksik olan tool çağrıları: hangi tool'lar tekrar çağrılıyor, hangi çağrılar boşa dönüyor, bir iş
kaç turda bitiyor, context'i asıl ne dolduruyor. Bu, roadmap §1'in (eski konuşmalarda arama)
büyümüş hâli ve §2'nin (ölçüm) devamı.

## Ölçülen malzeme

| | dosya | bayt | kayıt | tool çağrısı | tam tarama |
|---|---|---|---|---|---|
| Claude | 1.887 | 1,53 GB | 286.251 | 57.708 | 20,4 sn |
| Codex | 3.118 | 1,40 GB | 290.358 | 48.304 | 22,1 sn |

Bozuk satır: 576 bin kayıtta **sıfır**.

## Tasarımı değiştiren beş bulgu

1. **Tool çağrılarının %43'ü bugünkü tarayıcının hiç açmadığı dosyalarda.** `discoverFiles`
   (`internal/tokens/sources.go:97`) yalnız `<proje>/<uuid>.jsonl` derinliğini alıyor; oysa 1.887
   dosyanın yalnız 140'ı ana oturum, 1.745'i `subagents/` ve `workflows/` altında. Claude'un
   57.708 tool çağrısından **24.630'u** o dosyalarda. → İndeks bu ağacı taramak ZORUNDA.
2. **Bu bir tool-sonuç indeksi, mesaj indeksi değil.** Mesaj metni her iki külliyatın %2–4'ü
   (78 MB); tool sonuçları **%45'i (1,35 GB)**. Sonuç boyutları: Claude p50 260 B / p99 271 KB /
   max 683 KB; Codex p50 5,9 KB / p99 40 KB / max 15,7 MB. → Tam metin DEĞİL, sınırlı alıntı
   (2 KB) + ayrı `output_bytes` sütunu; kesilme görünür kalsın.
3. **Codex'te `name` alanı yalan söylüyor.** Çağrıların çoğu `exec` adını taşıyor ama girdisi
   shell komutu değil, `tools.X` çağıran **JavaScript**. Gerçek dağılım ancak açıldığında
   görünüyor: `web__run` 18.044, `exec_command` 9.246 (+4.952 doğrudan), `apply_patch` 1.614.
   Yani Codex'in baskın eylemi web, shell değil — ham `name` ile tam tersi görünüyor. Üstelik
   çağrıların %2,1'i tek kayıtta **birden çok** eylem taşıyor (max 7). → Açma işi YAZMA anında
   yapılacak; bir kayıt ≠ bir eylem.
4. **Reasoning metni HİÇBİR külliyatta yok.** Claude'un 35.315 `thinking` bloğu boş (baytlar
   `signature`); Codex'in 50.548 `reasoning` kaydı `encrypted_content`. Şemaya bakıp
   `reasoning_text` sütunu açmak, backfill anında boş çıkar. → Yalnız varlık/adet/bayt tutulur.
5. **Asıl zor kısım format değil, atıf.** Codex %97,5 çözülüyor (`session_meta.cwd`, 3.118
   dosyanın hepsinde var). Claude **%42**: 140 ana oturumun 59'u `customTitle` ile bir agent
   adına oturuyor, **67'si (%48) hiçbir yoldan atfedilemiyor**. Alt-agent dosyalarının kendi
   kimliği hiç yok, ebeveynden miras alıyorlar.

## Kararlar

### (a) Ayrı indeks, ortak tarama

`internal/tokens` 12 MB'lık, `bp usage`/bar'ı her çağrıda besleyen bir zaman-serisi deposu;
3 GB'lık ilişkisel + tam metin yükü onu bozar. **Ayrı indeks.** Ama tarayıcı yeniden yazılmayacak:
`readCompleteLines` + `checkpoint{Size,Offset,MTime}` (aynı dosya, satır 20 ve 111) bayt-ofset
sürdürmeyi iki külliyat için zaten çözmüş. Yeni yürüyüş bu parçaları kullanır, yorumlama tek
yerde kalır — "iki kopya ayrışırsa hangisi doğru kaybolur" dersi burada da geçerli.

**Depo: SQLite, `sqlite3` ikilisi dışarıdan çalıştırılarak.** Gerekçe kısıt: `go.mod` sıfır
bağımlılıklı ve `make release` dört hedefi `CGO_ENABLED=0` ile derliyor — cgo sürücüsü darwin
çapraz derlemesini kırar, saf-Go sürücü ~5 MB'lık bağımlılığı laptop ikililerine sokar. bp zaten
`tmux`/`ssh`/`codex` çalıştırıyor; `sqlite3` de öyle olur, `go.mod` boş kalır. Bedeli: indeksleyen
makinede tek paket kurulumu (şu an KURULU DEĞİL) ve o yoksa özelliğin kapanması.

**v1'de artımlılık YOK.** Tam yeniden inşa tek çekirdekte 42 saniye (Go'da daha hızlı). Checkpoint
mantığı hazır dursun, ama v1 basit kalsın; artımlılık ölçülen bir ihtiyaç doğunca eklenir.

### (b) Tek şema değil: dar bir ortak olay tablosu + külliyata özel tablolar

Gerçekten örtüşen tek şey tool çağrısı: her iki tarafta çağrı↔sonuç eşleşmesi **id üzerinden
%100** (Claude 57.752/57.752, Codex 48.268/48.268 — eşleşmeyen sıfır) ve süre türetilebilir.

```
tool_call(corpus, session_id, agent, file, offset, ts, tool, input_excerpt,
          output_excerpt, output_bytes, duration_ms, is_error, ordinal)
```

`tool` sütunu Codex'in `exec` sarmalayıcısı açılmış hâlidir; yoksa liderlik tablosu anlamsız olur.

Geri kalan zorlanmadan birleşmez ve zorlanırsa her satırda ~15 sütun boş kalır: Claude'un mesaj
DAG'ı (`uuid`/`parentUuid`/`requestId`) ile Codex'in tur modeli (`turn_id` + `task_complete`) ortak
bir soyutlamaya oturmuyor. → `claude_message` ve `codex_event` ayrı; ortak görünüm olay düzeyinde.

**Aynı isim, farklı anlam (tuzaklar, şemaya not düşülecek):** `model` Claude'da her assistant
kaydında, Codex'te yalnız `turn_context`'te (27× örnekleme farkı — naif `GROUP BY model` Claude'u
ağırlıklandırır); `timestamp` Claude kayıtlarının %73,7'sinde var (yan kayıtlarda ve
`journal.jsonl`'de yok — `NOT NULL` külliyatın dörtte birini sessizce düşürür); `duration_ms`
Claude'da insan izin bekleme süresini içerir, Codex'te çoğunlukla içermez (3.119 oturumun 3.076'sı
`codex_exec`); `is_error` yalnız Claude'da var ve %37 oranında "yok" (yokluk false sayılacak);
`session_id` Claude'da ana oturum + tüm alt-agent dosyalarını kapsar, Codex'te dosya başına birebir.

### (c) v1'in cevaplayacağı üç soru

Üçü de ölçülmüş veriyle destekleniyor, hiçbiri uydurma sütun gerektirmiyor:

1. **Hangi çağrılar boşa gidiyor?** Aynı oturumda aynı tool+girdi tekrarı, hata oranı (Claude'da
   %4,4, 57.752'de 2.550), boş/kısa dönen sonuçlar. Doğrudan token tasarrufu.
2. **Zaman nereye gidiyor?** Türetmeye gerek yok, ikisi de hazır veriyor: Claude
   `system/turn_duration.durationMs` (5.136 kayıt, p50 71 sn, p90 392 sn), Codex
   `task_complete.duration_ms` + `time_to_first_token_ms` (3.653 kayıt, p50 110 sn).
   Tool başına süre de var (Claude Bash p50 174 ms, WebSearch p50 8,1 sn).
3. **Context'i ne dolduruyor?** Oturum başına tool başına `output_bytes` toplamı — külliyatın
   %45'ini açıklayan sütun. Doğrudan compact politikasını (§4) ve agent talimatlarını besler.

Bedava gelen dördüncü: mesaj metni yalnız 78 MB olduğu için FTS5 ile **roadmap §1'in aramasını**
aynı indeks karşılar.

### Komut yüzeyi

`bp tokens` muhasebedir (maliyet), bu davranıştır: ayrı fiil — **`bp trace`**, alt komutlarla
(`index`, `tools`, `repeats`, `turns`, `search`). Roadmap §1'deki `bp search` ayrı bir fiil
olarak açılmaz, `bp trace search` olur.

### Gizlilik

İndeks WhatsApp içeriğini, kimlik bilgilerini ve kişisel veriyi tek bir aranabilir yerde toplar.
`state/` altında, 0600, makineyi terk etmez; federation yüzeyine **açılmaz** (`fed.expose`
kapsamına asla girmez), `bp trace` yalnız yereldir.

## Aşamalar

1. Yürüyüş + şema + `bp trace index` (tam yeniden inşa; alt-agent ağacı dahil).
2. Üç soru: `bp trace tools|repeats|turns`.
3. `bp trace search` (FTS5).
4. (Ölçülen ihtiyaç doğarsa) artımlı indeksleme, mevcut checkpoint tipleriyle.

## Bu iş sırasında çıkan, buraya ait olmayan iki bulgu

- **Token muhasebesinde delik olabilir:** `discoverFiles` alt-agent/workflow dosyalarını hiç
  açmıyor (checkpoint'te 3.258 girdinin sıfırı `subagents/` altında). O dosyalarda 73.563 kayıt
  var ve bir kısmı `usage` taşıyor → `bp usage`/`bp tokens` alt-agent tüketimini eksik sayıyor
  olabilir. Ayrı iş olarak doğrulanmalı; muhasebe geçmişini değiştireceği için bu planla
  karıştırılmayacak.
- **`maxRolloutScan = 200` artık bir günün altında:** 2026/08/01 gün dizininde 961 dosya var.
  Küresel mtime sıralaması sayesinde canlı oturum hâlâ öne çıkıyor, ama pay eridi.
