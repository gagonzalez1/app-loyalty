-- deploy: expand
ALTER TABLE sucursales
    ADD COLUMN perfil_publico jsonb
    CHECK (jsonb_typeof(perfil_publico) = 'object');
