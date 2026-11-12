-- deploy: expand
ALTER TABLE branch_menu_documents
 DROP CONSTRAINT branch_menu_documents_page_count_check,
 ADD CONSTRAINT branch_menu_documents_page_count_check CHECK (page_count >= 1);
