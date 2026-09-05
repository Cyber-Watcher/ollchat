# Службы systemd

Юнит службы описывается файлом .service в /etc/systemd/system. Секция [Unit] задаёт описание и зависимости After и Requires, секция [Service] — команду ExecStart, пользователя User и политику перезапуска Restart=on-failure, секция [Install] — цель WantedBy=multi-user.target.

После правки юнита выполняют systemctl daemon-reload, затем systemctl enable --now имя для включения при загрузке и запуска сразу. Состояние показывает systemctl status, журнал — journalctl -u имя -f.

Переменные окружения задаются Environment= или файлом EnvironmentFile=. Таймер .timer запускает службу по расписанию вместо cron: OnCalendar=*-*-* 23:00:00.
