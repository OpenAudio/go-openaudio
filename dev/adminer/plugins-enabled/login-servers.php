<?php
require_once('plugins/login-servers.php');

return new AdminerLoginServers([
    "[Node 1] openaudio-1" => [
        "driver" => "pgsql",
        "server" => "openaudio-1",
        "username" => "postgres",
        "password" => "postgres",
        "db" => "postgres"
    ],
    "[Node 2] openaudio-2" => [
        "driver" => "pgsql",
        "server" => "openaudio-2",
        "username" => "postgres",
        "password" => "postgres",
        "db" => "postgres"
    ],
    "[Node 3] openaudio-3" => [
        "driver" => "pgsql",
        "server" => "openaudio-3",
        "username" => "postgres",
        "password" => "postgres",
        "db" => "postgres"
    ],
    "[Node 4] openaudio-4" => [
        "driver" => "pgsql",
        "server" => "openaudio-4",
        "username" => "postgres",
        "password" => "postgres",
        "db" => "postgres"
    ]
]);
