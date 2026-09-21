ALTER TABLE marcas
    DROP CONSTRAINT marcas_icono_premio_check,
    DROP CONSTRAINT marcas_plantilla_tarjeta_check,
    DROP COLUMN icono_premio,
    DROP COLUMN plantilla_tarjeta;
