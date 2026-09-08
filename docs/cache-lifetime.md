# Cache ömrü: 5 Eylül 2026 incelemesi

`probot-egitim` için 36 dakika sıcak görünmesi 5 dakikalık bir cache'in yanlış
uzatılması değildi. Eşleşmiş Claude thread'i
`5677d046-d6e5-4799-b1c9-56b1b2a94116` son kullanım kaydında (15:44:09.062 UTC)
86.350 cache read, 3.743 cache write token bildirdi. Yazımın tamamı
`cache_creation.ephemeral_1h_input_tokens`; 5m yazımı sıfırdı.

[Claude Code belgesi](https://code.claude.com/docs/en/prompt-caching#cache-lifetime):
abonelik içindeki ana konuşma varsayılanı 1 saat; API/kredi kullanımı ve çoğu
yardımcı istek 5 dakika. İstek/ayar türüne göre değişir; plan adı tek başına kanıt
yerine kullanılmaz. [Claude API belgesi](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
okumaların süreyi yenilediğini, sürenin yanıtın bitişinden değil isteğin
başlangıcından sayıldığını belirtir.

[OpenAI belgesi](https://developers.openai.com/api/docs/guides/prompt-caching):
GPT-5.6 ve sonrası için `prompt_cache_options.ttl=30m`, son yazım/yeniden
kullanımdan en az 30 dakika. Eski modellerin `in_memory` seçeneğinde tipik süre
5–10 dakika; `24h` farklı bir politikadır. Bu API bilgisi Codex abonelik
oturumunun gerçek istek ayarını kanıtlamaz. Codex'in bp tarafından okunan
token_count kaydı TTL sağlamadığı için ona model adına bakarak süre atanmaz.

bp önceden tüm runtime'lara sabit 1 saat uyguluyordu. Artık son Claude isteğinin
tek süreli yazım dökümü varsa `warm~`/`cold~` gösterir; karışık veya eksik TTL,
yalnız okuma ve compact sonrası ölçümde `age` gösterir. `~` bir tahmindir:
transkriptteki ilk yanıt parçası isteğin gerçek başlangıcı olmayabilir ve sonraki
isteğin prefix/routing eşleşmesi garanti değildir. Aynı message ID'nin sonraki
parçaları cache saatini yenilemez. Gösterilen süre kalan ömür değildir.

Announce aynı runtime ölçümünü kullanır; yalnız tahminen sıcak hedefleri anında
teslime aday sayar, bilinmeyen/soğuk hedefleri biriktirir. Normal mesajlar mevcut
meşguliyet/input korumalarını kullanır. Yeni keepalive veya kota tüketen test yok.

Regresyonlar: 36 dakikalık 1h ve 5m yazımları, sınır dışı süre, karışık/eksik
TTL, read-only hit, compact, tekrar yazılan streaming mesajı ve bar çıktısı.
