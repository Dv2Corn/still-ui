#!/usr/bin/env python3
"""Generates site/{,ru/,zh/,ar/}index.html for still-ui from one template. Run from the repo root."""
import html
from pathlib import Path

INSTALL = 'bash &lt;(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh)'
GH = 'https://github.com/Dv2Corn/still-ui'
LANGS = [('en', '', 'English'), ('ru', 'ru/', 'Русский'), ('zh', 'zh/', '中文'), ('ar', 'ar/', 'العربية')]

T = {
 'en': dict(
  title='still-ui — set it up once, then leave it alone',
  desc='still-ui is a fork of the 3x-ui panel for Xray-core: clients that keep their settings, Xray that survives panel restarts, a panel that stays small on a 2 GB server.',
  docs='Docs', releases='Releases', readme='README.md', copy='Copy', copied='Copied',
  h1='Set it up once.<br>Then leave it alone.',
  lead='still-ui is a VPN panel for Xray-core, built from 3x-ui for servers that run for months on 2&nbsp;GB of RAM, with real people on mobile networks.',
  sub='Linux amd64 / arm64 · runs as root · prints the panel address, login and password at the end',
  alt_overview='The still-ui overview page',
  fixed_h='The things that used to hurt. Fixed.',
  fixed_p='Everything 3x-ui does still works: the same protocols, the same interface, the same database. still-ui changes what breaks on a small server after a few months.',
  cards=[
   ('Clients keep their settings', 'Updating a client through the API with only some fields no longer disables it and wipes its limits, Telegram ID and renewal settings.'),
   ('Restarting the panel drops nobody', 'Xray runs as its own sandboxed service. Update or restart the panel and Xray clients stay connected.'),
   ('The panel stays small', 'A memory cap from the installer and pooled AmneziaWG buffers: a runaway panel restarts instead of pushing the server into swap.'),
   ('iOS fingerprint and sing-box work again', 'A reproducible Xray build with a one-hunk REALITY patch accepts the short <code>ios</code> ClientHello that mobile networks let through.'),
   ('Certificates renew without you', 'The panel checks what each inbound really serves and restarts only Xray, only when a renewed certificate was not picked up.'),
   ('Local sites open directly', 'One setting: banks and government portals of your country open without the VPN, everything else goes through it. In every subscription format.'),
  ],
  meas_h='Measured, not promised.',
  meas_p='2 vCPU / 2 GiB server, 100 short AmneziaWG connections per second for 5 minutes, 3 runs each.', meas_link='Method and raw data',
  nums=[('403 → 176 MiB', 'Average panel memory under load, 3x-ui v3.11.0 vs still-ui.'),
        ('525 → 211 MiB', 'Peak panel memory in the same test.'),
        ('72 h', 'Mixed load with 200 users next to 3x-ui: no memory growth, goroutines and file descriptors flat, no restarts.')],
  mig_h='Already on 3x-ui? One command.',
  mig_p='The installer finds your 3x-ui database, moves inbounds, clients and subscriptions over and checks they are the same. Users keep their links. Another command brings 3x-ui back.',
  mig_sub='Marzban and old x-ui:',
  cmp_h='still-ui and 3x-ui, side by side.', cmp_p='Based on 3x-ui v3.11.0. Details for every line are in the README.',
  cmp_th=('In 3x-ui 3.11.0', 'In still-ui'),
  cmp=[('Since Xray 26.9 the server refuses the <code>ios</code> fingerprint and every sing-box client', 'A patched Xray build accepts them again'),
       ('Restarting or updating the panel drops every client', 'Xray is its own service; a panel restart drops no Xray client'),
       ('Panel memory grows under AmneziaWG traffic and can push a small server into swap', 'Reused buffers and a memory cap on the panel'),
       ('The Telegram bot sends the database unencrypted', 'Backups encrypted to your key, on a schedule, with a restore command'),
       ('A partial client update through the API disables the client and clears fields', 'Fields you do not send are kept'),
       ('A device over the HWID limit gets a bare 404', 'A clear notice in the app, a panel-wide default, old devices expire')],
  trust_h='Built to be checked.', trust_p='You run this on your server with root. You should not have to take our word for anything.',
  trust=[('Attested builds', 'Every release archive is built by GitHub Actions and carries a build attestation. <code>gh attestation verify</code> shows where it came from.'),
         ('Checked installer', 'Each release publishes its own <code>install.sh</code> in <code>SHA256SUMS</code>. The installer refuses an archive whose checksum is missing or wrong.'),
         ('A journal you can verify', 'Admin actions go to a hash-chained audit log, and the panel can check that no entry was changed or removed.')],
  see_h='See it.', alt_presets='Inbound presets for mobile networks', alt_client='A client card with its QR code',
  foot='still-ui is an independent fork of <a href="https://github.com/MHSanaei/3x-ui">3x-ui</a> by MHSanaei, under GPL-3.0. It is not affiliated with or endorsed by the 3x-ui authors.',
  issue='Report a problem', security='Security'),
 'ru': dict(
  title='still-ui — настроил один раз и забыл',
  desc='still-ui — форк панели 3x-ui для Xray-core: клиенты не теряют настройки, Xray переживает перезапуск панели, панель не разрастается на сервере с 2 ГБ.',
  docs='Документация', releases='Выпуски', readme='README.ru.md', copy='Копировать', copied='Скопировано',
  h1='Настроил один раз.<br>И забыл.',
  lead='still-ui — VPN-панель для Xray-core на основе 3x-ui. Для серверов, которые месяцами работают на 2&nbsp;ГБ памяти, а клиенты сидят в мобильных сетях.',
  sub='Linux amd64 / arm64 · запуск от root · в конце покажет адрес панели, логин и пароль',
  alt_overview='Главная страница still-ui',
  fixed_h='То, что раньше болело. Починено.',
  fixed_p='Всё, что умеет 3x-ui, работает: те же протоколы, тот же интерфейс, та же база. still-ui чинит то, что ломается на маленьком сервере через пару месяцев.',
  cards=[
   ('Клиенты не теряют настройки', 'Обновление клиента через API с частью полей больше не выключает его и не стирает лимиты, Telegram ID и продление.'),
   ('Перезапуск панели никого не отключает', 'Xray работает отдельной изолированной службой. Обновили или перезапустили панель — клиенты Xray остались на связи.'),
   ('Панель не разрастается', 'Потолок памяти от установщика и переиспользуемые буферы AmneziaWG: зависшая панель перезапускается, а не загоняет сервер в своп.'),
   ('Снова работают iOS-отпечаток и sing-box', 'Воспроизводимая сборка Xray с правкой REALITY в одну строку принимает короткий ClientHello <code>ios</code>, который пропускают мобильные сети.'),
   ('Сертификаты продлеваются сами', 'Панель проверяет, какой сертификат реально отдаёт каждый вход, и перезапускает только Xray и только если новый не подхватился.'),
   ('Российские сайты — напрямую', 'Одна настройка: банки и госуслуги открываются без VPN, остальное идёт через него. Во всех форматах подписки.'),
  ],
  meas_h='Измерено, а не обещано.',
  meas_p='Сервер 2 vCPU / 2 ГиБ, 100 коротких подключений AmneziaWG в секунду в течение 5 минут, по 3 прогона.', meas_link='Методика и сырые данные',
  nums=[('403 → 176 МиБ', 'Средняя память панели под нагрузкой: 3x-ui v3.11.0 против still-ui.'),
        ('525 → 211 МиБ', 'Пик памяти панели в том же тесте.'),
        ('72 ч', 'Смешанная нагрузка, 200 пользователей, рядом с 3x-ui: память не растёт, горутины и дескрипторы ровные, перезапусков нет.')],
  mig_h='Уже на 3x-ui? Одна команда.',
  mig_p='Установщик находит базу 3x-ui, переносит входы, клиентов и подписки и проверяет, что всё совпало. Ссылки у пользователей остаются прежними. Другая команда возвращает 3x-ui.',
  mig_sub='Marzban и старый x-ui:',
  cmp_h='still-ui и 3x-ui рядом.', cmp_p='Основа — 3x-ui v3.11.0. Подробности по каждой строке — в README.',
  cmp_th=('В 3x-ui 3.11.0', 'В still-ui'),
  cmp=[('С Xray 26.9 сервер отвергает отпечаток <code>ios</code> и всех клиентов на sing-box', 'Пропатченная сборка Xray снова их принимает'),
       ('Перезапуск или обновление панели отключает всех клиентов', 'Xray — отдельная служба, перезапуск панели клиентов Xray не трогает'),
       ('Память панели растёт под трафиком AmneziaWG и может загнать сервер в своп', 'Переиспользуемые буферы и потолок памяти для панели'),
       ('Telegram-бот отправляет базу без шифрования', 'Бэкапы зашифрованы вашим ключом, по расписанию, есть команда восстановления'),
       ('Частичное обновление клиента через API выключает его и стирает поля', 'Неотправленные поля сохраняются'),
       ('Устройство сверх лимита HWID получает голый 404', 'Понятное сообщение в приложении, лимит по умолчанию, старые устройства забываются')],
  trust_h='Сделано, чтобы проверять.', trust_p='Вы ставите это на свой сервер с правами root. Верить на слово не нужно.',
  trust=[('Подтверждённые сборки', 'Каждый архив выпуска собирает GitHub Actions и снабжает аттестацией. <code>gh attestation verify</code> покажет, откуда он.'),
         ('Проверяемый установщик', 'Каждый выпуск публикует свой <code>install.sh</code> в <code>SHA256SUMS</code>. Архив без верной контрольной суммы установщик не примет.'),
         ('Журнал, который можно проверить', 'Действия админов пишутся в журнал с цепочкой хешей, и панель умеет проверить, что ни одну запись не изменили и не удалили.')],
  see_h='Как это выглядит.', alt_presets='Заготовки входов для мобильных сетей', alt_client='Карточка клиента с QR-кодом',
  foot='still-ui — независимый форк <a href="https://github.com/MHSanaei/3x-ui">3x-ui</a> (автор MHSanaei), лицензия GPL-3.0. Проект не связан с авторами 3x-ui и не одобрен ими.',
  issue='Сообщить о проблеме', security='Безопасность'),
 'zh': dict(
  title='still-ui — 设置一次，从此省心',
  desc='still-ui 是基于 3x-ui 的 Xray-core 面板分支：客户端设置不丢失，Xray 在面板重启时不断线，2 GB 服务器上面板占用保持很小。',
  docs='文档', releases='发布', readme='README.zh.md', copy='复制', copied='已复制',
  h1='设置一次。<br>从此省心。',
  lead='still-ui 是基于 3x-ui 的 Xray-core VPN 面板，专为在 2&nbsp;GB 内存上连续运行数月、用户使用移动网络的服务器而打造。',
  sub='Linux amd64 / arm64 · 以 root 运行 · 结束时显示面板地址、用户名和密码',
  alt_overview='still-ui 概览页面',
  fixed_h='曾经的痛点，已经修好。',
  fixed_p='3x-ui 的一切照常可用：相同的协议、相同的界面、相同的数据库。still-ui 修复的是小服务器运行几个月后才会出现的问题。',
  cards=[
   ('客户端保留设置', '通过 API 只更新部分字段时，客户端不再被禁用，限额、Telegram ID 和续期设置也不再被清空。'),
   ('重启面板不掉线', 'Xray 作为独立的沙箱服务运行。更新或重启面板，Xray 客户端保持连接。'),
   ('面板保持轻量', '安装程序设置内存上限，AmneziaWG 缓冲区复用：失控的面板会被重启，而不是把服务器拖进 swap。'),
   ('iOS 指纹和 sing-box 重新可用', '可复现的 Xray 构建加上一处 REALITY 补丁，重新接受移动网络放行的短 <code>ios</code> ClientHello。'),
   ('证书自动续期生效', '面板检查每个入站实际提供的证书，仅在新证书未被加载时才只重启 Xray。'),
   ('本地网站直连', '一个设置：本国的银行和政府网站不走 VPN，其余流量走 VPN。适用于所有订阅格式。'),
  ],
  meas_h='实测数据，而非承诺。',
  meas_p='2 vCPU / 2 GiB 服务器，每秒 100 个短 AmneziaWG 连接，持续 5 分钟，各测 3 次。', meas_link='测试方法与原始数据',
  nums=[('403 → 176 MiB', '负载下面板平均内存：3x-ui v3.11.0 对比 still-ui。'),
        ('525 → 211 MiB', '同一测试中的面板内存峰值。'),
        ('72 小时', '200 个用户的混合负载，与 3x-ui 并行：内存无增长，goroutine 和文件描述符平稳，无重启。')],
  mig_h='已经在用 3x-ui？一条命令。',
  mig_p='安装程序会找到 3x-ui 数据库，迁移入站、客户端和订阅，并检查结果一致。用户的链接保持不变。另一条命令可以切回 3x-ui。',
  mig_sub='Marzban 和旧版 x-ui：',
  cmp_h='still-ui 与 3x-ui 对比。', cmp_p='基于 3x-ui v3.11.0。每一项的详情见 README。',
  cmp_th=('3x-ui 3.11.0', 'still-ui'),
  cmp=[('自 Xray 26.9 起，服务器拒绝 <code>ios</code> 指纹和所有 sing-box 客户端', '打过补丁的 Xray 构建重新接受它们'),
       ('重启或更新面板会断开所有客户端', 'Xray 是独立服务，重启面板不影响 Xray 客户端'),
       ('AmneziaWG 流量下面板内存增长，可能把小服务器拖进 swap', '缓冲区复用，并为面板设置内存上限'),
       ('Telegram 机器人发送未加密的数据库', '备份用你的密钥加密、按计划执行，并提供恢复命令'),
       ('通过 API 部分更新客户端会禁用它并清空字段', '未发送的字段会被保留'),
       ('超出 HWID 限制的设备只收到 404', '应用内清晰提示、面板级默认限制、旧设备自动过期')],
  trust_h='可以验证。', trust_p='你在自己的服务器上用 root 运行它，不必只听我们怎么说。',
  trust=[('可证明的构建', '每个发布包都由 GitHub Actions 构建并附带构建证明。<code>gh attestation verify</code> 可以查看来源。'),
         ('可校验的安装程序', '每个版本都在 <code>SHA256SUMS</code> 中发布自己的 <code>install.sh</code>。校验和缺失或不符的包会被拒绝。'),
         ('可验证的日志', '管理员操作写入哈希链审计日志，面板可以检查是否有记录被修改或删除。')],
  see_h='看看界面。', alt_presets='移动网络入站预设', alt_client='带二维码的客户端卡片',
  foot='still-ui 是 MHSanaei 的 <a href="https://github.com/MHSanaei/3x-ui">3x-ui</a> 的独立分支，采用 GPL-3.0 许可。本项目与 3x-ui 作者无关，也未获其认可。',
  issue='报告问题', security='安全'),
 'ar': dict(
  title='still-ui — اضبطه مرة واحدة ثم انسَه',
  desc='still-ui نسخة متفرعة من لوحة 3x-ui لـ Xray-core: العملاء يحتفظون بإعداداتهم، وXray يصمد عند إعادة تشغيل اللوحة، واللوحة تبقى خفيفة على خادم بذاكرة 2 GB.',
  docs='التوثيق', releases='الإصدارات', readme='README.md', copy='نسخ', copied='تم النسخ',
  h1='اضبطه مرة واحدة.<br>ثم انسَه.',
  lead='still-ui لوحة VPN لـ Xray-core مبنية على 3x-ui، لخوادم تعمل لأشهر بذاكرة 2&nbsp;GB ومستخدمين حقيقيين على شبكات الجوال.',
  sub='Linux amd64 / arm64 · يعمل بصلاحيات root · يعرض في النهاية عنوان اللوحة واسم المستخدم وكلمة المرور',
  alt_overview='صفحة النظرة العامة في still-ui',
  fixed_h='ما كان يؤلم، أصبح مُصلحًا.',
  fixed_p='كل ما يقدّمه 3x-ui ما زال يعمل: البروتوكولات نفسها والواجهة نفسها وقاعدة البيانات نفسها. يُصلح still-ui ما يتعطل على الخادم الصغير بعد بضعة أشهر.',
  cards=[
   ('العملاء يحتفظون بإعداداتهم', 'تحديث العميل عبر API ببعض الحقول فقط لم يعد يعطّله أو يمسح حدوده ومعرّف Telegram وإعدادات التجديد.'),
   ('إعادة تشغيل اللوحة لا تقطع أحدًا', 'يعمل Xray كخدمة مستقلة معزولة. حدّث اللوحة أو أعد تشغيلها ويبقى عملاء Xray متصلين.'),
   ('اللوحة تبقى خفيفة', 'سقف للذاكرة يضعه المثبّت ومخازن مؤقتة مُعاد استخدامها لـ AmneziaWG: اللوحة الخارجة عن السيطرة يُعاد تشغيلها بدل دفع الخادم إلى swap.'),
   ('بصمة iOS وsing-box تعملان من جديد', 'بناء قابل للتكرار لـ Xray مع تعديل من سطر واحد في REALITY يقبل رسالة ClientHello القصيرة <code>ios</code> التي تمررها شبكات الجوال.'),
   ('الشهادات تتجدد دونك', 'تتحقق اللوحة من الشهادة التي يقدمها كل مدخل فعليًا، وتعيد تشغيل Xray وحده فقط إذا لم تُحمَّل الشهادة المجددة.'),
   ('المواقع المحلية مباشرة', 'إعداد واحد: مواقع البنوك والجهات الحكومية في بلدك تفتح دون VPN، وكل ما عداها يمر عبره. في كل صيغ الاشتراك.'),
  ],
  meas_h='أرقام مقيسة، لا وعود.',
  meas_p='خادم 2 vCPU / 2 GiB، و100 اتصال AmneziaWG قصير في الثانية لمدة 5 دقائق، 3 مرات لكل منهما.', meas_link='المنهجية والبيانات الخام',
  nums=[('403 → 176 MiB', 'متوسط ذاكرة اللوحة تحت الحمل: 3x-ui v3.11.0 مقابل still-ui.'),
        ('525 → 211 MiB', 'ذروة ذاكرة اللوحة في الاختبار نفسه.'),
        ('72 ساعة', 'حمل مختلط بـ 200 مستخدم بجانب 3x-ui: لا نمو في الذاكرة، وعدد goroutines والواصفات ثابت، ولا إعادة تشغيل.')],
  mig_h='تستخدم 3x-ui؟ أمر واحد.',
  mig_p='يعثر المثبّت على قاعدة بيانات 3x-ui، وينقل المداخل والعملاء والاشتراكات ويتحقق من تطابقها. تبقى روابط المستخدمين كما هي، وأمر آخر يعيد 3x-ui.',
  mig_sub='Marzban وx-ui القديم:',
  cmp_h='still-ui و3x-ui جنبًا إلى جنب.', cmp_p='مبني على 3x-ui v3.11.0. تفاصيل كل سطر في README.',
  cmp_th=('في 3x-ui 3.11.0', 'في still-ui'),
  cmp=[('منذ Xray 26.9 يرفض الخادم بصمة <code>ios</code> وكل عملاء sing-box', 'بناء Xray المعدّل يقبلها من جديد'),
       ('إعادة تشغيل اللوحة أو تحديثها تقطع جميع العملاء', 'Xray خدمة مستقلة، وإعادة تشغيل اللوحة لا تقطع عملاء Xray'),
       ('ذاكرة اللوحة تنمو مع حركة AmneziaWG وقد تدفع الخادم الصغير إلى swap', 'مخازن مؤقتة مُعاد استخدامها وسقف لذاكرة اللوحة'),
       ('بوت Telegram يرسل قاعدة البيانات دون تشفير', 'نسخ احتياطية مشفّرة بمفتاحك وفق جدول، مع أمر للاستعادة'),
       ('التحديث الجزئي للعميل عبر API يعطّله ويمسح الحقول', 'الحقول غير المرسلة تبقى محفوظة'),
       ('الجهاز الذي يتجاوز حد HWID يتلقى 404 فقط', 'إشعار واضح في التطبيق، وحد افتراضي للوحة، والأجهزة القديمة تنتهي صلاحيتها')],
  trust_h='مصمم ليُتحقق منه.', trust_p='أنت تشغّله على خادمك بصلاحيات root. لا داعي لأن تأخذ كلامنا على محمل الثقة.',
  trust=[('بناء موثّق', 'كل أرشيف إصدار يبنيه GitHub Actions ويرفق به شهادة بناء. يُظهر <code>gh attestation verify</code> مصدره.'),
         ('مثبّت قابل للتحقق', 'ينشر كل إصدار ملف <code>install.sh</code> الخاص به في <code>SHA256SUMS</code>. يرفض المثبّت أي أرشيف مجموعه الاختباري مفقود أو خاطئ.'),
         ('سجل يمكن التحقق منه', 'تُسجَّل إجراءات المشرفين في سجل تدقيق بسلسلة تجزئة، ويمكن للوحة التحقق من أن أي سجل لم يُعدَّل أو يُحذف.')],
  see_h='شاهده.', alt_presets='إعدادات جاهزة للمداخل على شبكات الجوال', alt_client='بطاقة عميل مع رمز QR',
  foot='still-ui نسخة متفرعة مستقلة من <a href="https://github.com/MHSanaei/3x-ui">3x-ui</a> للمطور MHSanaei، بترخيص GPL-3.0. لا يرتبط المشروع بمؤلفي 3x-ui ولا يحظى بتأييدهم.',
  issue='الإبلاغ عن مشكلة', security='الأمان'),
}


def page(lang, prefix):
    t = T[lang]
    up = '../' if prefix else ''
    m = up + 'media/'
    sel = ' selected'
    langs = ''.join(f'<option value="{(up + p) or "./"}"{sel if l == lang else ""} lang="{l}">{n}</option>' for l, p, n in LANGS)
    alt = ''.join(f'<link rel="alternate" hreflang="{l}" href="https://dv2corn.github.io/still-ui/{p}">' for l, p, _ in LANGS)
    cards = ''.join(f'<div class="card"><h3>{a}</h3><p>{b}</p></div>' for a, b in t['cards'])
    nums = ''.join(f'<div class="card"><span class="num">{a}</span><p>{b}</p></div>' for a, b in t['nums'])
    cmp = ''.join(f'<tr><td>{a}</td><td>{b}</td></tr>' for a, b in t['cmp'])
    trust = ''.join(f'<div class="card"><h3>{a}</h3><p>{b}</p></div>' for a, b in t['trust'])
    btn = lambda i: (f'<button onclick="navigator.clipboard.writeText(document.getElementById(\'{i}\').textContent);'
                     f'this.textContent=\'{t["copied"]}\'">{t["copy"]}</button>')
    rtl = ' dir="rtl"' if lang == 'ar' else ''
    return f'''<!doctype html>
<html lang="{lang}"{rtl}>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{t['title']}</title>
<meta name="description" content="{html.escape(t['desc'])}">
<meta property="og:title" content="still-ui">
<meta property="og:description" content="{html.escape(t['desc'])}">
<meta property="og:image" content="https://dv2corn.github.io/still-ui/media/still-ui-overview-dark.png">
{alt}
<link rel="stylesheet" href="{up}style.css">
</head>
<body>
<div class="w">
<nav><b>still-ui</b><span><a href="{GH}#readme">{t['docs']}</a><a href="{GH}/releases/latest">{t['releases']}</a><a href="{GH}">GitHub</a><select class="lang" aria-label="Language" onchange="location.href=this.value">{langs}</select></span></nav>

<header>
  <h1>{t['h1']}</h1>
  <p class="lead">{t['lead']}</p>
  <div class="cmd" dir="ltr"><code id="c">{INSTALL}</code>{btn('c')}</div>
  <p class="sub">{t['sub']}</p>
  <picture class="shot">
    <source srcset="{m}still-ui-mobile-dark.png" media="(max-width: 600px)">
    <source srcset="{m}still-ui-overview-dark.png" media="(prefers-color-scheme: dark)">
    <img src="{m}still-ui-overview-light.png" alt="{t['alt_overview']}" width="1440" height="900">
  </picture>
</header>

<section>
  <h2>{t['fixed_h']}</h2>
  <p class="sec-lead">{t['fixed_p']}</p>
  <div class="grid">{cards}</div>
</section>

<section>
  <h2>{t['meas_h']}</h2>
  <p class="sec-lead">{t['meas_p']} <a href="{GH}/tree/main/bench/awg-relay-churn">{t['meas_link']}</a>.</p>
  <div class="grid">{nums}</div>
</section>

<section>
  <h2>{t['mig_h']}</h2>
  <p class="sec-lead">{t['mig_p']}</p>
  <div class="cmd" dir="ltr"><code id="m">{INSTALL} migrate</code>{btn('m')}</div>
  <p class="sub" style="text-align:center">{t['mig_sub']} <code dir="ltr">still-ui import marzban /var/lib/marzban</code></p>
</section>

<section>
  <h2>{t['cmp_h']}</h2>
  <p class="sec-lead">{t['cmp_p']} <a href="{GH}/blob/main/{t['readme']}">README</a></p>
  <table>
    <tr><th>{t['cmp_th'][0]}</th><th>{t['cmp_th'][1]}</th></tr>
    {cmp}
  </table>
</section>

<section>
  <h2>{t['trust_h']}</h2>
  <p class="sec-lead">{t['trust_p']}</p>
  <div class="grid">{trust}</div>
</section>

<section>
  <h2>{t['see_h']}</h2>
  <div class="pair">
    <picture><source srcset="{m}still-ui-presets-dark.png" media="(prefers-color-scheme: dark)"><img src="{m}still-ui-presets-light.png" alt="{t['alt_presets']}" loading="lazy"></picture>
    <picture><source srcset="{m}still-ui-client-dark.png" media="(prefers-color-scheme: dark)"><img src="{m}still-ui-client-light.png" alt="{t['alt_client']}" loading="lazy"></picture>
  </div>
</section>

<footer>
  {t['foot']}<br>
  <a href="{GH}/issues">{t['issue']}</a> · <a href="{GH}/blob/main/SECURITY.md">{t['security']}</a>
</footer>
</div>
</body>
</html>
'''


for lang, prefix, _ in LANGS:
    out = Path('site') / prefix / 'index.html'
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(page(lang, prefix))
    print(out)
