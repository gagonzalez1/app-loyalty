ALTER TABLE marcas
    ADD COLUMN plantilla_tarjeta VARCHAR(40),
    ADD COLUMN icono_premio VARCHAR(80),
    ADD CONSTRAINT marcas_plantilla_tarjeta_check CHECK (
        plantilla_tarjeta IS NULL OR plantilla_tarjeta IN (
            'COMIC_HQ', 'MINIMAL_PRO', 'POP_BADGE', 'PREMIUM_GOLD_CHECKS', 'DARK_LUXURY_CHECKS',
            'PREMIUM_GOLD', 'DARK_LUXURY', 'POP_HEADER', 'COMIC_HQ_POINTS', 'MINIMAL_PRO_POINTS',
            'POP_BADGE_POINTS', 'NEON_PULSE', 'SUNSET_GRADIENT'
        )
    ),
    ADD CONSTRAINT marcas_icono_premio_check CHECK (
        icono_premio IS NULL OR icono_premio ~ '^[a-z0-9-]{1,80}$'
    );
