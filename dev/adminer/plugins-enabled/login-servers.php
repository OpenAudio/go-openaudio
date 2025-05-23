<?php
require_once('plugins/login-servers.php');

return new AdminerLoginServers([
    "[Node 1] audiusd-1" => [
        "driver" => "pgsql",
        "server" => "audiusd-1",
        "username" => "postgres",
        "password" => "postgres",
        "db" => "postgres"
    ],
    "[Node 2] audiusd-2" => [
        "driver" => "pgsql",
        "server" => "audiusd-2",
        "username" => "postgres",
        "password" => "postgres",
        "db" => "postgres"
    ],
    "[Node 3] audiusd-3" => [
        "driver" => "pgsql",
        "server" => "audiusd-3",
        "username" => "postgres",
        "password" => "postgres",
        "db" => "postgres"
    ],
    "[Node 4] audiusd-4" => [
        "driver" => "pgsql",
        "server" => "audiusd-4",
        "username" => "postgres",
        "password" => "postgres",
        "db" => "postgres"
    ],
    "[Node State Sync] audiusd-ss" => [
        "driver" => "pgsql",
        "server" => "audiusd-ss",
        "username" => "postgres",
        "password" => "postgres",
        "db" => "postgres"
    ]
]);
