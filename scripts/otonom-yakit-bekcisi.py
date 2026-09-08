#!/usr/bin/env python3
"""Review candidate: live activity from bp schema 2; timestamp-window usage.

No transcript-directory-to-agent inference. Unknown/stale/incomplete observations
cannot trigger a current-work alarm. Existing delivery behavior is preserved;
this file has NOT replaced the live /srv/server-main/bin script.
Claude weighted thresholds remain; Codex raw totals need their own approved
threshold before becoming notifications. bp_runtime_usage exposes both providers.
"""
import json, os, sys, time, shlex, datetime as dt
import subprocess
from bp_runtime_usage import inspect

DURUM = "/srv/server-main/.otonom-bekci.json"
HEDEF = "905380408148@s.whatsapp.net"   # Tuna'nin kendi sohbeti; --from tanimsiz gonderen
KOPRU_LOG = "/srv/whatsapp/bridge.log"  # oldugu icin hedef ACIKCA verilmeli (yoksa to=null)
PROJE = "/root/.claude/projects"
# Iki kademe. Tek esik yaniltir: 900k'lik tek tur atan agent da var, 200 kucuk tur atan da.
ACIL_TOKEN = 20_000_000   # son insan girdisinden beri bu kadar yakildiysa: kacak
ACIL_GOAL_6SA = 6         # 6 saatte bu kadar goal tetigi = kendini yeniden doguran dongu
UYARI_TOKEN = 8_000_000   # bu kadar yakildiysa VE uzun suredir insansizsa
UYARI_SAAT = 24
SOGUMA_SAAT = 6          # ayni agent icin tekrar uyarmadan once
TAZELIK_SAAT = 12        # olay zamanina gore kullanim penceresi (mtime degil)

def durum_oku():
    try:
        with open(DURUM) as f:
            return json.load(f)
    except Exception:
        return {}

def main():
    kuru = "--kuru" in sys.argv          # test: uyari gondermeden ne bulacagini yazdir
    simdi = dt.datetime.now(dt.timezone.utc)
    durum = durum_oku()
    # bp owns agent/thread identity and live activity. No cwd basename guesses.
    try:
        report = json.loads(subprocess.check_output(
            ["/usr/local/bin/bp", "status", "--json"], text=True, timeout=30))
        simdi = dt.datetime.now(dt.timezone.utc)
        observations = inspect(report, simdi, TAZELIK_SAAT)
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        print(f"bekci runtime verisi bilinmiyor: {exc}", file=sys.stderr)
        return 1
    bulgular = []
    bakilan = len(observations)
    atlanan = sum(not row["running"] for row in observations)
    kirpilan = sum(not row.get("usage", {}).get("complete", False) for row in observations)
    for row in observations:
        if not row["can_alert"]:
            continue
        usage = row["usage"]
        # Existing weighted thresholds apply only to Claude. Codex token totals
        # are a different unit: expose them in bp, never apply a Claude budget.
        if not usage["weighted_units"]:
            continue
        yakit, goal_6sa = usage["weighted_units"], usage["goal_6h"]
        human_age = row.get("last_human_age_seconds")
        saat = human_age / 3600 if human_age is not None else 0
        if goal_6sa >= ACIL_GOAL_6SA or yakit >= ACIL_TOKEN:
            kademe = "ACIL"
        elif yakit >= UYARI_TOKEN and human_age is not None and saat >= UYARI_SAAT:
            kademe = "uyari"
        else:
            continue
        bulgular.append(dict(agent=row["agent"], yakit=yakit, tur=usage["messages"],
                             goal=goal_6sa, goal_6sa=goal_6sa, saat=saat, kosul="",
                             kademe=kademe, belirsiz=human_age is None))

    if not kuru:
        # Kalp atisi: "uyari gelmedi" ile "bekci oldu" ayni gorunmesin — health-watch
        # bu dosyanin tazeligine bakiyor. (wa-ciftleme-rapor.timer'da ayni ders.)
        try:
            open("/srv/server-main/.otonom-bekci.kalp", "w").write(str(int(time.time())))
        except OSError:
            pass

    kapsam = (f"kapsam: {bakilan} gercek agent; {atlanan} aktif degil/bilinmiyor; "
              f"{kirpilan} eksik/eslesmeyen olcum. Kullanim penceresi: son {TAZELIK_SAAT} saat; "
              "surmekte olan is ve tarihsel kullanim ayri denetlendi")

    if not kuru:
        with open("/srv/server-main/otonom-bekci.log", "a") as f:
            f.write(f"[{dt.datetime.now().isoformat(timespec='seconds')}] "
                    f"{len(bulgular)} bulgu · {kapsam}\n")

    if not bulgular:
        if kuru:
            print(f"denetlenen aktif/eslesmis kapsamda esik asilmadi\n{kapsam}")
        return

    bulgular.sort(key=lambda b: -b["yakit"])
    gonderilecek = []
    for b in bulgular:
        onceki = durum.get(b["agent"], {})
        son = onceki.get("ts", 0)
        # Soguma: ayni agent icin 6 saatte bir; ama yanma ikiye katlandiysa hemen tekrar uyar.
        if time.time() - son < SOGUMA_SAAT * 3600 and b["yakit"] < onceki.get("yakit", 0) * 2:
            continue
        gonderilecek.append(b)

    if not gonderilecek:
        if kuru:
            print("esik asildi ama hepsi soguma penceresinde:",
                  ", ".join(b["agent"] for b in bulgular))
        return

    # Olcum ani basta: bu uyari kuyrukta bekleyip BAYAT teslim edilebilir ve
    # icindeki tur/token sayilari o an gecerli olmayabilir (2026-08-21 dersi).
    satirlar = [f"⚠️ OTONOM YANMA [otonom-bekci] (olcum: "
                f"{simdi.astimezone().strftime('%d %b %H:%M')}) "
                f"— aktif agentlarda zaman pencereli kullanim:", ""]
    for b in gonderilecek:
        ad = b["agent"]
        sure = "bilinmiyor" if b["belirsiz"] else f"{b['saat']:.0f} saattir"
        isaret = "🔴" if b["kademe"] == "ACIL" else "•"
        satirlar.append(f"{isaret} {ad}: {sure} son kullanici girdisi, {b['tur']} assistant mesaji, "
                        f"son {TAZELIK_SAAT} saatte ~{b['yakit']/1e6:.0f}M agirlikli birim")
        if b["goal_6sa"]:
            satirlar.append(f"  son 6 saatte {b['goal_6sa']} goal_status kaydi; tek basina yeniden baslatma kaniti degil")
        else:
            satirlar.append("  tetikleyici bu olcumden belirlenemiyor")

    satirlar += ["", "Incelemek icin server-main'e yaz: '<ad>'i durdur'."]
    mesaj = "\n".join(satirlar)

    if kuru:
        print(mesaj)
        return

    # TESLIM DOGRULAMASI (2026-08-21 dersi, iki kez ayni tuzak):
    # bp'nin rc=0 dondurmesi mesajin GITTIGI anlamina gelmiyor — ilk testte kopru
    # "hedef yok" deyip karantinaya aldi, betik ise "uyardim" diye durumu yazdi ve
    # soguma penceresini baslatti. Yani gercek bir kacak sessizce yutulacakti.
    # Bu yuzden: hedef ACIKCA verilir + kopru logundan cikis teyit edilir.
    # shlex.quote — json.dumps DEGIL: json.dumps ASCII disi her karakteri \uXXXX'e cevirir
    # ve mesaj WhatsApp'a ters-slash yigini olarak duser (2026-08-21, Tuna fark etti).
    # --from 'server-main': ULASILABILIR bir kimlik olmali (2026-08-21 dersi).
    # Once 'otonom-bekci' imzaliyordum; oyle bir tmux oturumu YOK, dolayisiyla kopru
    # bir gonderim hatasi bildirimi urettiginde o bildirim var olmayan bir hedefe
    # kuyruklanip sonsuza kadar bekliyordu (state/pending/otonom-bekci.jsonl'de bir
    # ornegini buldum). Bildirimin ulasmasi, imzanin sik gorunmesinden onemli:
    # bekcinin adi artik metnin ICINDE.
    rc = os.system(f"/usr/local/bin/bp wa send --to {HEDEF} --from server-main "
                   + shlex.quote(mesaj))
    teslim = False
    if rc == 0:
        for _ in range(10):
            time.sleep(1)
            try:
                with open(KOPRU_LOG, errors="replace") as f:
                    kuyruk = f.readlines()[-40:]
            except OSError:
                break
            if any("GONDERILDI" in s and "OTONOM YANMA" in s for s in kuyruk):
                teslim = True
                break
            if any("KARANTINA" in s and "otonom-bekci" in s for s in kuyruk):
                break
    if not teslim:
        with open("/srv/server-main/otonom-bekci.log", "a") as f:
            f.write(f"[{dt.datetime.now().isoformat(timespec='seconds')}] "
                    f"UYARI TESLIM EDILEMEDI (rc={rc}) — durum yazilmadi, sonraki kosuda tekrar denenir\n")
        sys.exit(1)
    for b in gonderilecek:
        durum[b["agent"]] = {"ts": time.time(), "yakit": b["yakit"]}
    tmp = DURUM + ".tmp"
    with open(tmp, "w") as f:
        json.dump(durum, f)
    os.replace(tmp, DURUM)

if __name__ == "__main__":
    sys.exit(main() or 0)
